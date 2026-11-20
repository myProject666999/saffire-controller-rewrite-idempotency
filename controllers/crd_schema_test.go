/*
Copyright 2020 Fairwinds

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License
*/

package controllers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestCRDStatusSchemaMatchesStatus guards the hand-written CRD manifest: it must
// remain parseable YAML and expose the results-based status schema.
func TestCRDStatusSchemaMatchesStatus(t *testing.T) {
	path := filepath.Join("..", "config", "crd", "bases", "saffire.fairwinds.com_alternateimagesources.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc map[string]interface{}
	require.NoError(t, yaml.Unmarshal(raw, &doc))

	spec, ok := doc["spec"].(map[string]interface{})
	require.True(t, ok)
	versions, ok := spec["versions"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, versions)

	schema, ok := versions[0].(map[string]interface{})["schema"].(map[string]interface{})
	require.True(t, ok)
	props, ok := schema["openAPIV3Schema"].(map[string]interface{})["properties"].(map[string]interface{})
	require.True(t, ok)
	statusProps, ok := props["status"].(map[string]interface{})["properties"].(map[string]interface{})
	require.True(t, ok)

	_, hasResults := statusProps["results"]
	assert.True(t, hasResults, "CRD status must declare results")
	_, hasSwitches := statusProps["switches"]
	assert.False(t, hasSwitches, "legacy switches schema must be removed")
}
