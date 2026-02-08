docker buildx create --use
docker buildx build --platform=linux/amd64,linux/arm64 --tag "devforth/wait0:latest" --tag "devforth/wait0:1.0.1" --push .