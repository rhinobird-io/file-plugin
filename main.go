package main

import (
	"code.google.com/p/go-uuid/uuid"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/emicklei/go-restful"
	"github.com/garyburd/redigo/redis"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

type File struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

type FileResource struct {
	dir       string
	redisPool *redis.Pool
}

func (f FileResource) Register(container *restful.Container) {
	ws := new(restful.WebService)
	ws.
		Path("/files").
		Consumes(restful.MIME_XML, restful.MIME_JSON).
		Produces(restful.MIME_JSON, restful.MIME_XML)

	ws.Route(ws.GET("/{id}").To(f.getFileInfo))
	ws.Route(ws.GET("/{id}/download").To(f.downloadFile))
	ws.Route(ws.POST("").To(f.createFile).Consumes("multipart/form-data"))

	container.Add(ws)
}

func (f FileResource) findFile(id string) (*File, error) {
	conn := f.redisPool.Get()
	defer conn.Close()
	serialized, err := redis.Bytes(conn.Do("GET", id))
	if err != nil {
		return nil, nil
	}
	var file File
	err = json.Unmarshal(serialized, &file)
	if err != nil {
		return nil, err
	}
	return &file, nil
}
func (f FileResource) downloadFile(request *restful.Request, response *restful.Response) {
	file, err := f.findFile(request.PathParameter("id"))
	if file == nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusNotFound, "File not found!")
		return
	}
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	response.Header().Add("Content-Disposition", fmt.Sprintf("attachment; filename=%s", file.Name))
	path := filepath.Join(f.dir, file.Id, file.Name)

	http.ServeFile(response, request.Request, path)
}
func (f FileResource) getFileInfo(request *restful.Request, response *restful.Response) {
	file, err := f.findFile(request.PathParameter("id"))
	if file == nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusNotFound, "File not found!")
		return
	}
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	response.WriteEntity(file)
}

func (f *FileResource) createFile(request *restful.Request, response *restful.Response) {
	file, header, err := request.Request.FormFile("file")
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusBadRequest, err.Error())
		return
	}
	defer file.Close()
	newFile := File{uuid.New(), header.Filename}

	path := filepath.Join(f.dir, newFile.Id)
	err = os.MkdirAll(path, os.ModePerm)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}

	out, err := os.Create(filepath.Join(path, newFile.Name))
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	defer out.Close()

	_, err = io.Copy(out, file)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}

	conn := f.redisPool.Get()
	defer conn.Close()
	serialized, err := json.Marshal(newFile)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	_, err = conn.Do("SET", newFile.Id, serialized)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	response.WriteHeader(http.StatusCreated)
	response.WriteEntity(newFile)
}

var (
	redisAddress   = flag.String("redis-address", ":6379", "Address to the Redis server")
	maxConnections = flag.Int("max-connections", 10, "Max connections to Redis")
	dir            = flag.String("dir", "/data", "File directory")
)

func main() {
	flag.Parse()

	log.Printf("Will connect redis server: %s", *redisAddress)
	log.Printf("Max connections: %d", *maxConnections)
	redisPool := redis.NewPool(func() (redis.Conn, error) {
		c, err := redis.Dial("tcp", *redisAddress)

		if err != nil {
			log.Println(err)
			return nil, err
		}

		return c, err
	}, *maxConnections)
	defer redisPool.Close()

	wsContainer := restful.NewContainer()
	f := FileResource{*dir, redisPool}
	f.Register(wsContainer)
	log.Printf("start listening on port " + os.Getenv("PORT"))
	server := &http.Server{Addr: ":" + os.Getenv("PORT"), Handler: wsContainer}
	log.Fatal(server.ListenAndServe())
}
