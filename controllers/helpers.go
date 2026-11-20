// Copyright 2020 FairwindsOps Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"fmt"
	"strings"
)

// parseImageString returns the repository and version of the image string.
// It only handles plain "repository:tag" forms. Digest-pinned images
// (repo@sha256:...), implicit :latest images and registry hosts with ports
// are not split here and return an error; callers treat that as undecidable
// and leave the container untouched.
func parseImageString(image string) (string, string, error) {
	parsed := strings.Split(image, ":")
	if len(parsed) != 2 {
		return "", "", fmt.Errorf("could not parse image string: %s", image)
	}
	return parsed[0], parsed[1], nil
}

// getNewImage returns a new image string by finding a new repository from the list
// of equivalentRepositories and appending the version to it.
func getNewImage(image string, equivalentRepositories []string) (string, error) {
	oldRepository, version, err := parseImageString(image)
	if err != nil {
		return "", err
	}

	if !containsString(equivalentRepositories, oldRepository) {
		return "", fmt.Errorf("image repository was not found in equivalentRepositories")
	}

	for _, newRepository := range equivalentRepositories {
		if newRepository != oldRepository {
			return fmt.Sprintf("%s:%s", newRepository, version), nil
		}
	}
	return "", fmt.Errorf("unable to find next repository to use")
}

// containsString reports whether the slice contains the given string.
func containsString(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
}
