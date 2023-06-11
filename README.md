# \[G\]o\[PT\]est

## Installation
`go install github.com/sentiens/goptest@latest`

## Usage
1. First generate specification for the tested code:
```goptest -cases=true -what="Add function" -spec-file=specs.yaml -code-files=testcode.go```

2. Review and edit `specs.yml` code
3. Run to generate tests code 
```goptest -spec-file=specs.yaml -code-files=testcode.go -output-file=generated_test.go``` 

