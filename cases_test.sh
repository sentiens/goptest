#!/bin/bash

go build main.go
./main -cases=true -what="Add function" -spec-file=specs.yaml -code-files=testcode.go
