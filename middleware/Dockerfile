FROM golang:1.20-alpine

# Install necessary packages
RUN apk update && apk add --no-cache git

# Set working directory
WORKDIR /app

# Copy go.mod and go.sum
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the application
RUN go build -o middleware .

# Expose necessary ports (if any)
EXPOSE 8080

# Set environment variables
ENV PORT=8080

# Run the application
CMD ["./middleware"]
