#!/bin/bash

go build main.go
./main -spec-file=specs.yaml -code-files=testcode.go -output-file=generated_test.go
