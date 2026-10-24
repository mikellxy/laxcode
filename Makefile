.PHONY: build build-web build-cloud vet test clean
build:
	./deploy/build.sh
build-web:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build
build-cloud:
	./deploy/build.sh
vet:
	go vet ./...
test:
	go test ./...
	pnpm --dir web test
	python3 -m unittest discover -s deploy -p '*_test.py'
clean:
	rm -rf release
