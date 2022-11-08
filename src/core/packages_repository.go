package core

import (
	"ahkpm/src/utils"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/otiai10/copy"
)

type PackagesRepository interface {
	CopyPackage(dep ResolvedDependency, path string) error
	GetPackageDependencies(dep ResolvedDependency) (*DependencySet, error)
	GetResolvedDependencySHA(dep Dependency) (string, error)
	ClearCache() error
	// For testing
	WithRemoveAll(removeAll func(path string) error) PackagesRepository
}

type packagesRepository struct {
	removeAll func(path string) error
}

func NewPackagesRepository() PackagesRepository {
	return &packagesRepository{
		removeAll: os.RemoveAll,
	}
}

func (pr *packagesRepository) WithRemoveAll(removeAll func(path string) error) PackagesRepository {
	pr.removeAll = removeAll
	return pr
}

func (pr *packagesRepository) CopyPackage(dep ResolvedDependency, path string) error {
	err := pr.ensurePackageIsReady(dep.Name, dep.SHA)
	if err != nil {
		return err
	}
	err = copy.Copy(pr.getPackageCacheDir(dep.Name), path, copy.Options{
		// Skip the .git directory since it isn't needed at the destination
		Skip: func(src string) (bool, error) {
			return strings.HasSuffix(src, ".git"), nil
		},
	})
	if err != nil {
		return errors.New("Error copying package to target module directory")
	}
	return nil
}

func (pr *packagesRepository) GetPackageDependencies(dep ResolvedDependency) (*DependencySet, error) {
	err := pr.ensurePackageIsReady(dep.Name, dep.SHA)
	if err != nil {
		return nil, err
	}
	manifestPath := pr.getPackageCacheDir(dep.Name) + `/ahkpm.json`
	manifest, err := ManifestFromFile(manifestPath)

	deps := NewDependencySet()
	if err == nil {
		deps = manifest.Dependencies
	} else if !strings.HasPrefix(err.Error(), "Error reading") {
		return &deps, err
	}

	return &deps, nil
}

func (pr *packagesRepository) GetResolvedDependencySHA(dep Dependency) (string, error) {
	err := pr.ensurePackageIsReady(dep.Name(), dep.Version().Value())
	if err != nil {
		return "", err
	}
	repo, err := git.PlainOpen(pr.getPackageCacheDir(dep.Name()))
	if err != nil {
		return "", errors.New("Error opening package repository " + dep.Name())
	}
	ref, err := repo.Head()
	if err != nil {
		return "", errors.New("Error getting package repository HEAD" + dep.Name())
	}
	return ref.Hash().String(), nil
}

func (pr *packagesRepository) ClearCache() error {
	return pr.removeAll(pr.getCacheDir())
}

func (pr *packagesRepository) getCacheDir() string {
	return utils.GetAhkpmDir() + `\cache`
}

func (pr *packagesRepository) getPackageCacheDir(depName string) string {
	return pr.getCacheDir() + `\` + depName
}

func (pr *packagesRepository) ensurePackageIsReady(depName string, depVersionString string) error {
	packageCacheDir := pr.getPackageCacheDir(depName)

	err := os.MkdirAll(packageCacheDir, os.ModePerm)
	if err != nil {
		return errors.New("Error creating package cache directory")
	}

	packageCloneAlreadyExisted, err := utils.FileExists(packageCacheDir + `\.git`)
	if err != nil {
		return errors.New("Error checking if package was cloned")
	}

	if !packageCloneAlreadyExisted {
		// Clone the repository into the cache directory
		_, err := git.PlainClone(packageCacheDir, false, &git.CloneOptions{
			URL:               getGitUrl(depName),
			RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		})
		if err != nil {
			message := "Error cloning package"
			if err.Error() == "authentication required" {
				message = "Error downloading package " + depName + ". Are you sure that package exists?"
			}
			return errors.New(message)
		}
	}

	// Checkout the specified version
	repo, err := git.PlainOpen(packageCacheDir)
	if err != nil {
		return errors.New("Error opening package")
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return errors.New("Error getting worktree")
	}

	if packageCloneAlreadyExisted {
		errorMessage := "Problem fetching latest updates to package " + depName + ". Continuing from local cache."

		branches, err := repo.Branches()
		if err != nil {
			fmt.Println(errorMessage)
		}

		// Brute forcing our way to updating all branches. Ideally we'd only
		// do this for the branch we're checking out, but determining whether
		// we're checking out a branch requires larger scale changes.
		err = branches.ForEach(func(branch *plumbing.Reference) error {
			err = worktree.Checkout(&git.CheckoutOptions{
				Branch: branch.Name(),
				Force:  true, // Ignore changes in the working tree
			})
			if err != nil {
				return err
			}
			return worktree.Pull(&git.PullOptions{
				RemoteName:    "origin",
				ReferenceName: branch.Name(),
			})
		})

		if err != nil && err != git.NoErrAlreadyUpToDate {
			fmt.Println(errorMessage)
		}
	}

	hash, err := repo.ResolveRevision(plumbing.Revision(depVersionString))
	if err != nil {
		message := "Error resolving revision"
		if err.Error() == "reference not found" {
			message = "Could not find version " + depVersionString + " for package " + depName + ". Are you sure that version exists?"
		}
		return errors.New(message)
	}

	err = worktree.Checkout(&git.CheckoutOptions{
		Hash:  (*hash),
		Force: true, // Ignore changes in the working tree
	})
	if err != nil {
		fmt.Println(err.Error())
		return errors.New("Error checking out version")
	}

	submodules, err := worktree.Submodules()
	if err != nil {
		return errors.New("Error getting submodules")
	}

	for _, sub := range submodules {
		err := sub.Update(&git.SubmoduleUpdateOptions{})
		// In the event of a connection issue we continue from the local copy
		if err != nil && !strings.Contains(err.Error(), "no such host") {
			return errors.New("Error updating submodule")
		}
	}
	return nil
}

func getGitUrl(packageName string) string {
	return "https://" + packageName + ".git"
}
