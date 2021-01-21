	"sync"
	"github.com/google/go-cmp/cmp"
		DefaultBranch: &graphql.Branch{Name: "main", Target: graphql.Target{OID: "d34db33f"}},

			Target: graphql.Target{OID: "f00b4r3r"},
	changesetTemplateBranch := "my-branch"

	type filesByBranch map[string][]string
	type filesByRepository map[string]filesByBranch

		repos     []*graphql.Repository
		archives  []mockRepoArchive
		template  *ChangesetTemplate
		steps     []Step
		transform *TransformChanges
		wantFilesChanged  filesByRepository
		wantTitle         string
		wantBody          string
		wantCommitMessage string
		wantAuthorName    string
		wantAuthorEmail   string

		wantErrInclude string
			wantFilesChanged: filesByRepository{
				srcCLIRepo.ID: filesByBranch{
					changesetTemplateBranch: []string{"README.md", "main.go"},
				},
				sourcegraphRepo.ID: {
					changesetTemplateBranch: []string{"README.md"},
				},

			wantFilesChanged: filesByRepository{
				srcCLIRepo.ID: filesByBranch{
					changesetTemplateBranch: []string{"main.go", "modified-main.go.md", "added-modified-main.go.md"},
				},
			},
		},
		{
			name:  "empty",
			repos: []*graphql.Repository{srcCLIRepo},
			archives: []mockRepoArchive{
				{repo: srcCLIRepo, files: map[string]string{
					"README.md": "# Welcome to the README\n",
					"main.go":   "package main\n\nfunc main() {\n\tfmt.Println(     \"Hello World\")\n}\n",
				}},
			},
			steps: []Step{
				{Run: `true`, Container: "doesntmatter:13"},
			},
			// No changesets should be generated.
			wantFilesChanged: filesByRepository{},
		},
		{
			name:  "transform group",
			repos: []*graphql.Repository{srcCLIRepo, sourcegraphRepo},
			archives: []mockRepoArchive{
				{repo: srcCLIRepo, files: map[string]string{
					"README.md":  "# Welcome to the README\n",
					"a/a.go":     "package a",
					"a/b/b.go":   "package b",
					"a/b/c/c.go": "package c",
				}},
				{repo: sourcegraphRepo, files: map[string]string{
					"README.md":  "# Welcome to the README\n",
					"a/a.go":     "package a",
					"a/b/b.go":   "package b",
					"a/b/c/c.go": "package c",
				}},
			},
			steps: []Step{
				{Run: `echo 'var a = 1' >> a/a.go`, Container: "doesntmatter:13"},
				{Run: `echo 'var b = 2' >> a/b/b.go`, Container: "doesntmatter:13"},
				{Run: `echo 'var c = 3' >> a/b/c/c.go`, Container: "doesntmatter:13"},
			},
			transform: &TransformChanges{
				Group: []Group{
					{Directory: "a/b/c", Branch: "in-directory-c"},
					{Directory: "a/b", Branch: "in-directory-b", Repository: sourcegraphRepo.Name},
				},
			},
			wantFilesChanged: filesByRepository{
				srcCLIRepo.ID: filesByBranch{
					changesetTemplateBranch: []string{
						"a/a.go",
						"a/b/b.go",
					},
					"in-directory-c": []string{
						"a/b/c/c.go",
					},
				},
				sourcegraphRepo.ID: filesByBranch{
					changesetTemplateBranch: []string{
						"a/a.go",
					},
					"in-directory-b": []string{
						"a/b/b.go",
						"a/b/c/c.go",
					},
				},
			},
		},
		{
			name:  "templated changesetTemplate",
			repos: []*graphql.Repository{srcCLIRepo},
			archives: []mockRepoArchive{
				{repo: srcCLIRepo, files: map[string]string{
					"README.md": "# Welcome to the README\n",
					"main.go":   "package main\n\nfunc main() {\n\tfmt.Println(     \"Hello World\")\n}\n",
				}},
			},
			steps: []Step{
				{
					Run:       `go fmt main.go`,
					Container: "doesntmatter:13",
					Outputs: Outputs{
						"myOutputName1": Output{
							Value: "${{ index step.modified_files 0 }}",
						},
					},
				},
				{
					Run:       `echo -n "Hello World!"`,
					Container: "alpine:13",
					Outputs: Outputs{
						"myOutputName2": Output{
							Value:  `thisStepStdout: "${{ step.stdout }}"`,
							Format: "yaml",
						},
						"myOutputName3": Output{
							Value: "cool-suffix",
						},
					},
				},
			},
			template: &ChangesetTemplate{
				Title: "myOutputName1=${{ outputs.myOutputName1}}",
				Body: `myOutputName1=${{ outputs.myOutputName1}},myOutputName2=${{ outputs.myOutputName2.thisStepStdout }}
modified_files=${{ steps.modified_files }}
added_files=${{ steps.added_files }}
deleted_files=${{ steps.deleted_files }}
renamed_files=${{ steps.renamed_files }}
repository_name=${{ repository.name }}`,
				Branch: "templated-branch-${{ outputs.myOutputName3 }}",
				Commit: ExpandedGitCommitDescription{
					Message: "myOutputName1=${{ outputs.myOutputName1}},myOutputName2=${{ outputs.myOutputName2.thisStepStdout }}",
					Author: &GitCommitAuthor{
						Name:  "myOutputName1=${{ outputs.myOutputName1}}",
						Email: "myOutputName1=${{ outputs.myOutputName1}}",
					},
				},

			wantFilesChanged: filesByRepository{
				srcCLIRepo.ID: filesByBranch{
					"templated-branch-cool-suffix": []string{"main.go"},
				},
			},
			wantTitle: "myOutputName1=main.go",
			wantBody: `myOutputName1=main.go,myOutputName2=Hello World!
modified_files=[main.go]
added_files=[]
deleted_files=[]
renamed_files=[]
repository_name=github.com/sourcegraph/src-cli`,
			wantCommitMessage: "myOutputName1=main.go,myOutputName2=Hello World!",
			wantAuthorName:    "myOutputName1=main.go",
			wantAuthorEmail:   "myOutputName1=main.go",
			cache := newInMemoryExecutionCache()
			creator := &dockerBindWorkspaceCreator{dir: testTempDir}
				Cache:   cache,
				Creator: creator,
				RepoFetcher: &repoFetcher{
					client: client,
					dir:    testTempDir,
				},
			// execute contains the actual logic running the tasks on an
			// executor. We'll run this multiple times to cover both the cache
			// and non-cache code paths.
			execute := func() {
				executor := newExecutor(opts, client, featuresAllEnabled())
				template := &ChangesetTemplate{Branch: changesetTemplateBranch}
				if tc.template != nil {
					template = tc.template
				for _, r := range tc.repos {
					executor.AddTask(r, tc.steps, tc.transform, template)
				executor.Start(context.Background())
				specs, err := executor.Wait()
				if tc.wantErrInclude == "" && err != nil {
					t.Fatalf("execution failed: %s", err)
				}
				if err != nil && !strings.Contains(err.Error(), tc.wantErrInclude) {
					t.Errorf("wrong error. have=%q want included=%q", err, tc.wantErrInclude)
				}
				if tc.wantErrInclude != "" {
					return
				wantSpecs := 0
				for _, byBranch := range tc.wantFilesChanged {
					wantSpecs += len(byBranch)
				}
				if have, want := len(specs), wantSpecs; have != want {
					t.Fatalf("wrong number of changeset specs. want=%d, have=%d", want, have)
				for _, spec := range specs {
					if have, want := len(spec.Commits), 1; have != want {
						t.Fatalf("wrong number of commits. want=%d, have=%d", want, have)
					}

					attrs := []struct{ name, want, have string }{
						{name: "title", want: tc.wantTitle, have: spec.Title},
						{name: "body", want: tc.wantBody, have: spec.Body},
						{name: "commit.Message", want: tc.wantCommitMessage, have: spec.Commits[0].Message},
						{name: "commit.AuthorEmail", want: tc.wantAuthorEmail, have: spec.Commits[0].AuthorEmail},
						{name: "commit.AuthorName", want: tc.wantAuthorName, have: spec.Commits[0].AuthorName},
					}
					for _, attr := range attrs {
						if attr.want != "" && attr.have != attr.want {
							t.Errorf("wrong %q attribute. want=%q, have=%q", attr.name, attr.want, attr.have)
						}
					wantFiles, ok := tc.wantFilesChanged[spec.BaseRepository]
					if !ok {
						t.Fatalf("unexpected file changes in repo %s", spec.BaseRepository)
					}

					branch := strings.ReplaceAll(spec.HeadRef, "refs/heads/", "")
					wantFilesInBranch, ok := wantFiles[branch]
					if !ok {
						t.Fatalf("spec for repo %q and branch %q but no files expected in that branch", spec.BaseRepository, branch)
					}

					fileDiffs, err := diff.ParseMultiFileDiff([]byte(spec.Commits[0].Diff))
					if err != nil {
						t.Fatalf("failed to parse diff: %s", err)
					}

					if have, want := len(fileDiffs), len(wantFilesInBranch); have != want {
						t.Fatalf("repo %s: wrong number of fileDiffs. want=%d, have=%d", spec.BaseRepository, want, have)
					}

					diffsByName := map[string]*diff.FileDiff{}
					for _, fd := range fileDiffs {
						if fd.NewName == "/dev/null" {
							diffsByName[fd.OrigName] = fd
						} else {
							diffsByName[fd.NewName] = fd
						}
					}

					for _, file := range wantFilesInBranch {
						if _, ok := diffsByName[file]; !ok {
							t.Errorf("%s was not changed (diffsByName=%#v)", file, diffsByName)
						}

			verifyCache := func() {
				want := len(tc.repos)
				if tc.wantErrInclude != "" {
					want = 0
				}

				// Verify that there is a cache entry for each repo.
				if have := cache.size(); have != want {
					t.Errorf("unexpected number of cache entries: have=%d want=%d cache=%+v", have, want, cache)
				}
			}

			// Sanity check, since we're going to be looking at the side effects
			// on the cache.
			if cache.size() != 0 {
				t.Fatalf("unexpectedly hot cache: %+v", cache)
			}

			// Run with a cold cache.
			t.Run("cold cache", func(t *testing.T) {
				execute()
				verifyCache()
			})

			// Run with a warm cache.
			t.Run("warm cache", func(t *testing.T) {
				execute()
				verifyCache()
			})
func TestValidateGroups(t *testing.T) {
	repoName := "github.com/sourcegraph/src-cli"
	defaultBranch := "my-campaign"

	tests := []struct {
		defaultBranch string
		groups        []Group
		wantErr       string
	}{
		{
			groups: []Group{
				{Directory: "a", Branch: "my-campaign-a"},
				{Directory: "b", Branch: "my-campaign-b"},
			},
			wantErr: "",
		},
		{
			groups: []Group{
				{Directory: "a", Branch: "my-campaign-SAME"},
				{Directory: "b", Branch: "my-campaign-SAME"},
			},
			wantErr: "transformChanges would lead to multiple changesets in repository github.com/sourcegraph/src-cli to have the same branch \"my-campaign-SAME\"",
		},
		{
			groups: []Group{
				{Directory: "a", Branch: "my-campaign-SAME"},
				{Directory: "b", Branch: defaultBranch},
			},
			wantErr: "transformChanges group branch for repository github.com/sourcegraph/src-cli is the same as branch \"my-campaign\" in changesetTemplate",
		},
	}

	for _, tc := range tests {
		err := validateGroups(repoName, defaultBranch, tc.groups)
		var haveErr string
		if err != nil {
			haveErr = err.Error()
		}

		if haveErr != tc.wantErr {
			t.Fatalf("wrong error:\nwant=%q\nhave=%q", tc.wantErr, haveErr)
		}
	}
}

func TestGroupFileDiffs(t *testing.T) {
	diff1 := `diff --git 1/1.txt 1/1.txt
new file mode 100644
index 0000000..19d6416
--- /dev/null
+++ 1/1.txt
@@ -0,0 +1,1 @@
+this is 1
`
	diff2 := `diff --git 1/2/2.txt 1/2/2.txt
new file mode 100644
index 0000000..c825d65
--- /dev/null
+++ 1/2/2.txt
@@ -0,0 +1,1 @@
+this is 2
`
	diff3 := `diff --git 1/2/3/3.txt 1/2/3/3.txt
new file mode 100644
index 0000000..1bd79fb
--- /dev/null
+++ 1/2/3/3.txt
@@ -0,0 +1,1 @@
+this is 3
`

	defaultBranch := "my-default-branch"
	allDiffs := diff1 + diff2 + diff3

	tests := []struct {
		diff          string
		defaultBranch string
		groups        []Group
		want          map[string]string
	}{
		{
			diff: allDiffs,
			groups: []Group{
				{Directory: "1/2/3", Branch: "everything-in-3"},
			},
			want: map[string]string{
				"my-default-branch": diff1 + diff2,
				"everything-in-3":   diff3,
			},
		},
		{
			diff: allDiffs,
			groups: []Group{
				{Directory: "1/2", Branch: "everything-in-2-and-3"},
			},
			want: map[string]string{
				"my-default-branch":     diff1,
				"everything-in-2-and-3": diff2 + diff3,
			},
		},
		{
			diff: allDiffs,
			groups: []Group{
				{Directory: "1", Branch: "everything-in-1-and-2-and-3"},
			},
			want: map[string]string{
				"my-default-branch":           "",
				"everything-in-1-and-2-and-3": diff1 + diff2 + diff3,
			},
		},
		{
			diff: allDiffs,
			groups: []Group{
				// Each diff is matched against each directory, last match wins
				{Directory: "1", Branch: "only-in-1"},
				{Directory: "1/2", Branch: "only-in-2"},
				{Directory: "1/2/3", Branch: "only-in-3"},
			},
			want: map[string]string{
				"my-default-branch": "",
				"only-in-3":         diff3,
				"only-in-2":         diff2,
				"only-in-1":         diff1,
			},
		},
		{
			diff: allDiffs,
			groups: []Group{
				// Last one wins here, because it matches every diff
				{Directory: "1/2/3", Branch: "only-in-3"},
				{Directory: "1/2", Branch: "only-in-2"},
				{Directory: "1", Branch: "only-in-1"},
			},
			want: map[string]string{
				"my-default-branch": "",
				"only-in-1":         diff1 + diff2 + diff3,
			},
		},
		{
			diff: allDiffs,
			groups: []Group{
				{Directory: "", Branch: "everything"},
			},
			want: map[string]string{
				"my-default-branch": diff1 + diff2 + diff3,
			},
		},
	}

	for _, tc := range tests {
		have, err := groupFileDiffs(tc.diff, defaultBranch, tc.groups)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !cmp.Equal(tc.want, have) {
			t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(tc.want, have))
		}
	}
}

		path := fmt.Sprintf("/%s@%s/-/raw", archive.repo.Name, archive.repo.BaseRef())

// inMemoryExecutionCache provides an in-memory cache for testing purposes.
type inMemoryExecutionCache struct {
	cache map[string]ExecutionResult
	mu    sync.RWMutex
}

func newInMemoryExecutionCache() *inMemoryExecutionCache {
	return &inMemoryExecutionCache{
		cache: make(map[string]ExecutionResult),
	}
}

func (c *inMemoryExecutionCache) size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.cache)
}

func (c *inMemoryExecutionCache) Get(ctx context.Context, key ExecutionCacheKey) (ExecutionResult, bool, error) {
	k, err := key.Key()
	if err != nil {
		return ExecutionResult{}, false, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if res, ok := c.cache[k]; ok {
		return res, true, nil
	}
	return ExecutionResult{}, false, nil
}

func (c *inMemoryExecutionCache) Set(ctx context.Context, key ExecutionCacheKey, result ExecutionResult) error {
	k, err := key.Key()
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache[k] = result
	return nil
}

func (c *inMemoryExecutionCache) Clear(ctx context.Context, key ExecutionCacheKey) error {
	k, err := key.Key()
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.cache, k)
	return nil
}