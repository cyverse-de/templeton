FROM golang:1.11-alpine

RUN apk add --no-cache git
RUN go get -u github.com/jstemmer/go-junit-report

COPY . /go/src/github.com/cyverse-de/templeton
ENV CGO_ENABLED=0
RUN go install -v -ldflags "-X main.appver=$version -X main.gitref=$git_commit" github.com/cyverse-de/templeton

ENTRYPOINT ["templeton"}

ARG git_commit=unknown
ARG version="2.9.0"
ARG descriptive_version=unknown

LABEL org.cyverse.git-ref="$git_commit"
LABEL org.cyverse.version="$version"
LABEL org.cyverse.descriptive-version="$descriptive_version"

EXPOSE 60000
LABEL org.label-schema.vcs-ref="$git_commit"
LABEL org.label-schema.vcs-url="https://github.com/cyverse-de/templeton"
LABEL org.label-schema.version="$descriptive_version"
