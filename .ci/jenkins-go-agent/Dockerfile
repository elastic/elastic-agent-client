FROM golang:1.14.4

RUN \
    apt-get -qq -y update \
      && apt-get install -qq -y --no-install-recommends \
         zip rsync golang-goprotobuf-dev\
      && rm -rf /var/lib/apt/lists/*

RUN go get -u github.com/magefile/mage \
  && go get -u github.com/elastic/go-licenser \
  && go get -u -v golang.org/x/tools/cmd/goimports \
  && go get -u -v golang.org/x/lint/golint \
  && go get -u github.com/golang/protobuf/proto \
  && go get -u github.com/golang/protobuf/protoc-gen-go \
  && go get -v -u github.com/jstemmer/go-junit-report

ENV GO111MODULE="auto"
