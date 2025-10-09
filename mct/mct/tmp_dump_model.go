package main

import (
	"fmt"
	"github.com/tursomari/machtiani/mct/llm"
)

func main() {
	model, err := llm.ResolveModel("test-model")
	if err != nil {
		panic(err)
	}
	fmt.Printf("Alias=%q Provider=%q Model=%q Endpoint=%q\n", model.Alias, model.ProviderName, model.Model, model.Endpoint)
}
