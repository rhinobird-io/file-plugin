FROM golang:1.4

RUN apt-get update -y && apt-get install nfs-common rpcbind -y
ADD . /go/src/rbfile

RUN cd /go/src/rbfile && GOPATH="/go/src/rbfile/Godeps/_workspace":$GOPATH go install -v .

EXPOSE 8080

CMD service rpcbind start && mkdir data && mount ${NFS_PATH} data && /go/bin/rbfile -redis-address=$REDIS_IP:$REDIS_PORT --dir=data
