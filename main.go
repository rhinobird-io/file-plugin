package main

import (
	"github.com/pborman/uuid"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/emicklei/go-restful"
	"github.com/garyburd/redigo/redis"
	"github.com/liuyang1204/go-progress"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"mime/multipart"
	"os"
	"time"
	"strings"
)

type File struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	Status string `json:"status"`
	Progress float32 `json:"progress"`
	Url string `json:"url"`
}

type FileResource struct {
	weedUrl       string
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
	ws.Route(ws.GET("/{id}/fetch").To(f.downloadFile))
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

	resp, err := http.Get(file.Url)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	for k, v := range resp.Header {
		response.Header().Set(k, v[0])
	}
	response.WriteHeader(resp.StatusCode)
	
	if strings.HasSuffix(request.SelectedRoutePath(), "download") {
		response.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", file.Name))
	}
	io.Copy(response.ResponseWriter, resp.Body)
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
	w := response.ResponseWriter
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, "Streaming unsupported!")
		return
	}
	ticker := time.NewTicker(time.Millisecond * 100)
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
		fmt.Fprintf(w, "data: {\"type\": \"done\", \"content\": \"%s\"}\n\n", file.Status)
		flusher.Flush()
	}

	<-notify
	ticker.Stop()
}

type WeedInfo struct {
	Fid string `json:"fid"`
	Url string `json:"url"`
}

func (f *FileResource) createFile(request *restful.Request, response *restful.Response) {
	file := new(File)
	err := request.ReadEntity(&file)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusBadRequest, err.Error())
		log.Println(err)
		return
	}
	file.Id = uuid.New()
	file.Status = "init"
	resp, err := http.Post(f.weedUrl+"/dir/assign", "text/plain", nil)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusBadRequest, err.Error())
		log.Println(err)
		return
	}
	decoder := json.NewDecoder(resp.Body)
	var info WeedInfo
	err = decoder.Decode(&info)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusBadRequest, err.Error())
		log.Println(err)
		return
	}
	file.Url = fmt.Sprintf("http://%s/%s", info.Url, info.Fid)
	conn := f.redisPool.Get()
	defer conn.Close()
	serialized, err := json.Marshal(file)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		log.Println(err)
		return
	}
	_, err = conn.Do("SET", file.Id, serialized)
	if err != nil {
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		log.Println(err)
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
	saveFile := func() error {
		serialized, err := json.Marshal(fileInfo)
		if err != nil {
			return err
		}
		_, err = conn.Do("SET", fileInfo.Id, serialized)
		if err != nil {
			return err
		}
		return nil
	}
	fileInfo.Status = "uploading"
	fileInfo.Progress = 0
	err = saveFile()
	if err != nil{
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	progressReader := progress.NewProgressReader(file, request.Request.ContentLength)
	ticker := time.NewTicker(time.Millisecond * 100)
	defer ticker.Stop()
	go func() {
		for _ = range ticker.C {
    		   fileInfo.Progress = progressReader.Progress()
		   err = saveFile()
			if err != nil{
				response.AddHeader("Content-Type", "text/plain")
				response.WriteErrorString(http.StatusInternalServerError, err.Error())
				return
			}
        }
	}()
	go func() {
		part, err := writer.CreateFormFile("file", header.Filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		_, err = io.Copy(part, progressReader)
		if err != nil {
			writer.Close()
			pw.CloseWithError(err)
			return
		}
		writer.Close()
		pw.Close()
	}()
	r, err := http.NewRequest("PUT", fileInfo.Url, pr)
	if err != nil{
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	r.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{}
	res, err := client.Do(r)
	if err != nil{
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	if res.StatusCode >= 400 {
		response.AddHeader("Content-Type", "text/plain")
		str, err := ioutil.ReadAll(res.Body)
		if err != nil {
			response.AddHeader("Content-Type", "text/plain")
			response.WriteErrorString(http.StatusInternalServerError, err.Error())
			return
		}
		response.WriteErrorString(res.StatusCode, string(str))
		return
	}
	fileInfo.Status = "uploaded"
	fileInfo.Progress = 100
	err = saveFile()
	if err != nil{
		response.AddHeader("Content-Type", "text/plain")
		response.WriteErrorString(http.StatusInternalServerError, err.Error())
		return
	}
	response.WriteHeader(http.StatusOK)
	response.WriteEntity(fileInfo)
}

var (
	redisAddress   = flag.String("redis-address", ":6379", "Address to the Redis server")
	maxConnections = flag.Int("max-connections", 10, "Max connections to Redis")
	weedUrl        = flag.String("weed-master-url", "localhost:9393", "Weed master URL")
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
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
	f := FileResource{*weedUrl, redisPool}
	f.Register(wsContainer)
	log.Printf("start listening on port " + os.Getenv("PORT"))
	server := &http.Server{Addr: ":" + os.Getenv("PORT"), Handler: wsContainer}
	log.Fatal(server.ListenAndServe())
}
