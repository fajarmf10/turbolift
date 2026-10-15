/*
 * Copyright 2026 Fajar Maulana Firdaus.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * https://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestItDefaultsToGitHub(t *testing.T) {
	t.Setenv(EnvVar, "")

	assert.False(t, IsGitLab())
	assert.NoError(t, Validate())
}

func TestItSelectsGitLabIgnoringCase(t *testing.T) {
	t.Setenv(EnvVar, "GitLab")

	assert.True(t, IsGitLab())
	assert.NoError(t, Validate())
}

func TestItAcceptsGitHubExplicitly(t *testing.T) {
	t.Setenv(EnvVar, "github")

	assert.False(t, IsGitLab())
	assert.NoError(t, Validate())
}

func TestItRejectsAnUnknownPlatform(t *testing.T) {
	t.Setenv(EnvVar, "gitlba")

	assert.False(t, IsGitLab())
	err := Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `"gitlba"`)
}

func TestItSplitsAGitLabPathWithNestedGroups(t *testing.T) {
	host, namespace, repo, err := SplitGitLabPath("group/subgroup/repo")

	assert.NoError(t, err)
	assert.Equal(t, "", host)
	assert.Equal(t, "group/subgroup", namespace)
	assert.Equal(t, "repo", repo)
}

func TestItSplitsAGitLabPathWithAHost(t *testing.T) {
	host, namespace, repo, err := SplitGitLabPath("gitlab.example.com/group/subgroup/repo")

	assert.NoError(t, err)
	assert.Equal(t, "gitlab.example.com", host)
	assert.Equal(t, "group/subgroup", namespace)
	assert.Equal(t, "repo", repo)
}

func TestItReadsADottedFirstPartOfATwoPartPathAsAGroup(t *testing.T) {
	host, namespace, repo, err := SplitGitLabPath("my.group/repo")

	assert.NoError(t, err)
	assert.Equal(t, "", host)
	assert.Equal(t, "my.group", namespace)
	assert.Equal(t, "repo", repo)
}

func TestItRejectsAGitLabPathWithoutANamespace(t *testing.T) {
	for _, line := range []string{"repo", "group//repo", "/group/repo", "group/repo/", "gitlab.example.com/repo/"} {
		_, _, _, err := SplitGitLabPath(line)
		assert.Error(t, err, line)
	}
}
