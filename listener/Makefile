.PHONY: build clean

PLUGIN_NAME = beacon_listener_serverless.so

build:
	CGO_ENABLED=1 go build -buildmode=plugin -o $(PLUGIN_NAME)

clean:
	rm -f $(PLUGIN_NAME)
