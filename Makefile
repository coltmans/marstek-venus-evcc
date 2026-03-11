# Set PI_HOST to your Raspberry Pi user@ip, e.g: make deploy PI_HOST=pi@192.168.1.100
PI_HOST=pi@raspberrypi.local

build:
	CGO_ENABLED=0 go build -o marstek-api .

build-pi:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o marstek-api-arm64 .

run:
	CGO_ENABLED=0 go run .

deploy: build-pi
	scp marstek-api-arm64 $(PI_HOST):/tmp/marstek-api
	ssh $(PI_HOST) "sudo mv /tmp/marstek-api /usr/local/bin/marstek-api && sudo systemctl restart marstek-api && sudo systemctl status marstek-api --no-pager"

test:
	curl -s http://localhost:7071/api/status | jq .
	curl -s http://localhost:7071/api/soc    | jq .
	curl -s http://localhost:7071/api/power  | jq .
