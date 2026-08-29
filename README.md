# L7 Ingress Controller

L7 Reverse Proxy to dynamically route backend requests

# Usage (WIP)
```bash
# Set up testing environment
cd cmd/backend
docker compose up -d
# Start reverse proxy
cd ../proxy
docker build -t proxy -p 8080:8080 .
docker run proxy
# Ensure everything works
curl localhost:8080/users/api
```