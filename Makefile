TARGET := gh-repos

.PHONY: build clean

build: $(TARGET)

$(TARGET): main.go
	go build -o $@ .

clean:
	rm -f $(TARGET)
