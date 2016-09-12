FROM golang:1.7-alpine

ARG git_commit=unknown
ARG version="5.2.8.0"

LABEL org.cyverse.git-ref="$git_commit"
LABEL org.cyverse.version="$version"

COPY . /go/src/github.com/cyverse-de/templeton
RUN go install -v -ldflags "-X main.appver=$version -X main.gitref=$git_commit" github.com/cyverse-de/templeton

EXPOSE 60000
ENTRYPOINT ["templeton"]
CMD ["--help"]
