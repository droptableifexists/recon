FROM golang:1.21

WORKDIR /github/workspace
COPY . .

RUN go build -o recon .

CMD ["./recon"]
