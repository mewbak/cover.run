package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	msgpack "gopkg.in/vmihailenco/msgpack.v2"

	"github.com/go-redis/cache"
	"github.com/go-redis/redis"
	"github.com/gorilla/mux"
	"github.com/nuveo/gofn"
	"github.com/nuveo/gofn/provision"
	"github.com/urfave/negroni"
)

const (
	DEFAULT_TAG = "golang-1.10"
)

func imageSupported(tag string) bool {
	switch tag {
	case
		"golang-1.10",
		"golang-1.9",
		"golang-1.8":
		return true
	}
	return false
}

type Object struct {
	Repo   string
	Tag    string
	Cover  string
	Output bool
}

func redisConn() (ring *redis.Ring, codec *cache.Codec) {
	ring = redis.NewRing(&redis.RingOptions{
		Addrs: map[string]string{
			"server1": "redis:6379",
		},
	})
	codec = &cache.Codec{
		Redis: ring,

		Marshal: func(v interface{}) ([]byte, error) {
			return msgpack.Marshal(v)
		},
		Unmarshal: func(b []byte, v interface{}) error {
			return msgpack.Unmarshal(b, v)
		},
	}
	return
}

func repoCover(repo, imageTag string) (obj Object) {
	_, codec := redisConn()
	cacheKey := fmt.Sprintf("%s-%s", repo, imageTag)
	obj.Repo = repo
	obj.Tag = imageTag
	if !imageSupported(imageTag) {
		obj.Cover = fmt.Sprintf("Sorry, not found docker image avelino/cover.run:%s, see Supported languages: https://github.com/avelino/cover.run#supported", imageTag)
		return
	}
	if err := codec.Get(cacheKey, &obj); err != nil {
		StdOut, StdErr := run("avelino/cover.run", imageTag, repo)
		stdOut := strings.Trim(StdOut, " \n")
		obj.Cover = StdErr
		obj.Output = false
		if stdOut != "" {
			obj.Cover = stdOut
			obj.Output = true
		}
		codec.Set(&cache.Item{
			Key:        cacheKey,
			Object:     obj,
			Expiration: time.Hour,
		})
	}
	return
}

func HandlerRepoJSON(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		tag = DEFAULT_TAG
	}
	obj := repoCover(vars["repo"], tag)
	json.NewEncoder(w).Encode(obj)
}

type copier struct {
	transport http.RoundTripper
}

func (c copier) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := c.transport.RoundTrip(request)
	return response, err
}

type Repository struct {
	Repo  string
	Tag   string
	Cover string
}

func repoLatest() (repos []Repository) {
	ring, codec := redisConn()
	keys, err := ring.Keys("*").Result()
	if err != nil {
		log.Println(err)
	}

	var obj Object
	for _, key := range keys {
		if len(repos) == 5 {
			return
		}
		if err := codec.Get(key, &obj); err == nil {
			if obj.Output {
				repos = append(repos, Repository{obj.Repo, obj.Tag, obj.Cover})
			}
		}
	}
	return
}

func HandlerRepoSVG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-CoverRunProxy", "CoverRunProxy")
	w.Header().Set("cache-control", "priviate, max-age=0, no-cache")
	w.Header().Set("pragma", "no-cache")
	w.Header().Set("expires", "-1")

	vars := mux.Vars(r)
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		tag = DEFAULT_TAG
	}
	obj := repoCover(vars["repo"], tag)
	cover, _ := strconv.ParseFloat(strings.Replace(obj.Cover, "%", "", -1), 64)
	var color string
	if cover >= 70 {
		color = "green"
	} else if cover >= 45 {
		color = "yellow"
	} else {
		color = "red"
	}

	SHIELDS := "https://img.shields.io/badge/cover.run-%s-%s.svg?style=flat-square"
	badge := strings.Replace(fmt.Sprintf(SHIELDS, obj.Cover, color), "%", "%25", 1)

	http.Redirect(w, r, badge, http.StatusTemporaryRedirect)
	return
}

func HandlerRepo(w http.ResponseWriter, r *http.Request) {
	Body := map[string]interface{}{}
	vars := mux.Vars(r)
	repo := vars["repo"]
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		tag = DEFAULT_TAG
	}
	Body["Repo"] = repo
	obj := repoCover(repo, tag)
	Body["Cover"] = obj.Cover
	Body["Tag"] = obj.Tag
	Body["repositories"] = repoLatest()
	t := template.Must(template.ParseFiles("./templates/layout.tmpl", "./templates/repo.tmpl"))
	t.Execute(w, Body)
	return
}

func Handler(w http.ResponseWriter, r *http.Request) {
	t := template.Must(template.ParseFiles("./templates/layout.tmpl", "./templates/home.tmpl"))

	Body := map[string]interface{}{}
	Body["repositories"] = repoLatest()

	t.Execute(w, Body)
	return
}

func main() {
	n := negroni.Classic()
	r := mux.NewRouter()
	r.HandleFunc("/", Handler)
	r.HandleFunc("/go/{repo:.*}.json", HandlerRepoJSON)
	r.HandleFunc("/go/{repo:.*}.svg", HandlerRepoSVG)
	r.HandleFunc("/go/{repo:.*}", HandlerRepo)
	r.PathPrefix("/assets").Handler(
		http.StripPrefix("/assets", http.FileServer(http.Dir("./assets/"))))
	n.UseHandler(r)
	n.Run(":3000")
}

func run(imageRepoName, dockerTag, repo string) (StdOut, StdErr string) {
	buildOpts := &provision.BuildOptions{
		DoNotUsePrefixImageName: true,
		ImageName:               strings.ToLower(fmt.Sprintf("%s:%s", imageRepoName, dockerTag)),
		StdIN:                   fmt.Sprintf("sh /run.sh %s", repo),
	}
	containerOpts := &provision.ContainerOptions{}
	StdOut, StdErr, err := gofn.Run(
		buildOpts, containerOpts)
	if err != nil {
		log.Println(err)
	}

	return
}
