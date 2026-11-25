package cmd

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/artifactbrand"
)

// RunArtifactBrandList prints every brand profile with its firm name.
func RunArtifactBrandList(w io.Writer) error {
	root, err := artifactbrand.Dir()
	if err != nil {
		return err
	}
	return listBrands(w, root)
}

func listBrands(w io.Writer, root string) error {
	names, err := artifactbrand.ListIn(root)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		_, err := fmt.Fprintf(w, "No brands. Create one as %s%c with a brand.json.\n", filepath.Join(root, "<name>"), filepath.Separator)
		return err
	}
	for _, n := range names {
		b, err := artifactbrand.LoadFrom(root, n)
		if err != nil {
			fmt.Fprintf(w, "%s\t(invalid: run `agentctl artifact brand check %s`)\n", n, n)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\n", n, b.Firm)
	}
	return nil
}

// RunArtifactBrandCheck validates a brand profile, printing every problem. It
// returns an error (so the exit status is non-zero) when there is any.
func RunArtifactBrandCheck(w io.Writer, name string) error {
	root, err := artifactbrand.Dir()
	if err != nil {
		return err
	}
	return checkBrand(w, root, name)
}

func checkBrand(w io.Writer, root, name string) error {
	b, err := artifactbrand.LoadFrom(root, name)
	var errs artifactbrand.Errors
	if errors.As(err, &errs) {
		for _, e := range errs {
			fmt.Fprintln(w, e.String())
		}
		return fmt.Errorf("brand %q has %d problem(s)", name, len(errs))
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "brand %q (%s) is valid\n", name, b.Firm)
	return err
}
