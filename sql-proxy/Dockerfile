FROM golang:1.21

# Build arguments
ARG LISTEN_PORT=5433
ARG BACKEND_HOST=postgres
ARG BACKEND_PORT=5432
ARG DB_CONNECTION_STRING=host=localhost port=5432 user=postgres password=postgres sslmode=disable
# Environment variables
ENV LISTEN_PORT=${LISTEN_PORT}
ENV BACKEND_HOST=${BACKEND_HOST}
ENV BACKEND_PORT=${BACKEND_PORT}
ENV DB_CONNECTION_STRING=${DB_CONNECTION_STRING}

WORKDIR /app
COPY . .

RUN go build -o proxy .

EXPOSE ${LISTEN_PORT} 8080

CMD ["./proxy"]
