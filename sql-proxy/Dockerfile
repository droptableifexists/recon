FROM golang:1.21

WORKDIR /app
COPY . .

RUN go build -o proxy .

EXPOSE 5433 8080

RUN ls -la /app

CMD ["./proxy"]
