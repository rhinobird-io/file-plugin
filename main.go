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
	"time"
)

type File struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	Status string `json:"status"`
	Progress float32 `json:"progress"`
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

	ws.Route(ws.GET("/{id}").To(f.getFileInfo).Produces("text/event-stream"))
	ws.Route(ws.GET("/{id}/download").To(f.downloadFile))
	ws.Route(ws.POST("").To(f.createFile))
	ws.Route(ws.PUT("/{id}").To(f.uploadFile).Consumes("multipart/form-data"))

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
	log.Println("test")
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
	w := response.ResponseWriter
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, "Streaming unsupported!")
		return
	}
	ticker := time.NewTicker(time.Second)
	notify := w.(http.CloseNotifier).CloseNotify()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprintf(w, "data: {\"type\": \"name\", \"content\": \"%s\"}\n\n", file.Name)
	flusher.Flush()
	if file.Status == "uploading" || file.Status == "init" {
		for _ = range ticker.C {
    		   fileInfo, err := f.findFile(request.PathParameter("id"))
			
			if fileInfo == nil {
				fmt.Fprintf(w, "data: {\"type\": \"error\", \"content\": \"not found\"}\n\n")
				flusher.Flush()
			}
			if err != nil {
				fmt.Fprintf(w, "data: {\"type\": \"error\", \"content\": \"%s\"}\n\n", err.Error())
				flusher.Flush()
			}
		    if fileInfo.Status == "uploading" || file.Status == "init" {
				fmt.Fprintf(w, "data: {\"type\": \"progress\", \"content\": \"%f\"}\n\n", fileInfo.Progress)
				flusher.Flush()
			}
			if fileInfo.Status == "uploaded" || fileInfo.Status == "failed" {
				fmt.Fprintf(w, "data: {\"type\": \"done\", \"content\": \"%s\"}\n\n", fileInfo.Status)
				flusher.Flush()
				ticker.Stop()
			}
        }
	} else {
		fmt.Printf("writing")
		fmt.Fprintf(w, "data: {\"type\": \"done\", \"content\": \"%s\"}\n\n", file.Status)
		flusher.Flush()
	}

	<-notify
	ticker.Stop()
}

func (f *FileResource) createFile(request *restful.Request, response *restful.Response) {
	file := new(File)
	err := request.ReadEntity(&file)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusBadRequest, err.Error())
		return
	}
	file.Id = uuid.New()
	file.Status = "init"
	
	conn := f.redisPool.Get()
	defer conn.Close()
	serialized, err := json.Marshal(file)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	_, err = conn.Do("SET", file.Id, serialized)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	response.WriteHeader(http.StatusOK)
	response.WriteEntity(file)
}

func (f *FileResource) uploadFile(request *restful.Request, response *restful.Response) {
	fileInfo, err := f.findFile(request.PathParameter("id"))

	if fileInfo == nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusNotFound, "File not found!")
		return
	}
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	file, header, err := request.Request.FormFile("file")
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusBadRequest, err.Error())
		return
	}
	defer file.Close()
	if fileInfo.Name != header.Filename {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusBadRequest, "File name does not match!")
		return
	}
	conn := f.redisPool.Get()
	defer conn.Close()
	saveFile := func() {
		serialized, err := json.Marshal(fileInfo)
		if err != nil {
			response.AddHeader("Content-Type", "text/plain")
			response.WriteErrorString(http.StatusInternalServerError, err.Error())
			return
		}
		_, err = conn.Do("SET", fileInfo.Id, serialized)
		if err != nil {
			response.AddHeader("Content-Type", "text/plain")
			response.WriteErrorString(http.StatusInternalServerError, err.Error())
			return
		}
	}
	fileInfo.Status = "uploading"
	fileInfo.Progress = 0
	saveFile()

	path := filepath.Join(f.dir, fileInfo.Id)
	err = os.MkdirAll(path, os.ModePerm)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}

	out, err := os.Create(filepath.Join(path, fileInfo.Name))
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	defer out.Close()

    length := request.Request.ContentLength
	var read int64
    var p float32
	ticker := time.NewTicker(time.Second)
	go func() {
		for _ = range ticker.C {
    		   fileInfo.Progress = p
		   saveFile()
        }
	}()
    for {
            buffer := make([]byte, 100000)
            cBytes, err := file.Read(buffer)
            if err == io.EOF {
                    break
            } else if err != nil {
				response.AddHeader("Content-Type", "text/plain")
				response.WriteErrorString(http.StatusInternalServerError, err.Error())
				return
			}
            read = read + int64(cBytes)

            if read > 0 {
                    p = float32(read*100) / float32(length)
				    out.Write(buffer[0:cBytes])
            } else {
                    break
            }

    }
	ticker.Stop()
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}

	fileInfo.Status = "uploaded"
	fileInfo.Progress = 100
	saveFile()
	response.WriteHeader(http.StatusOK)
	response.WriteEntity(fileInfo)
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
