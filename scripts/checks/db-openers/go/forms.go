//go:build ignore

package main

import (
	"go/types"
	"sort"
	"strings"
)

// pgsetting is the import path of the one formatter.
const pgsetting = "github.com/sjawhar/envoy/pgsetting"

// formatterReads are pgsetting's functions that read a connection string (their second argument)
// before any opener: a value one of them read is read by the formatter.
var formatterReads = map[string]bool{
	pgsetting + ".Refuse":          true,
	pgsetting + ".ParseConfig":     true,
	pgsetting + ".ParsePoolConfig": true,
}

// formatterAnswers are pgsetting's functions whose answer is a configuration the formatter read.
var formatterAnswers = map[string]bool{
	pgsetting + ".ParseConfig":     true,
	pgsetting + ".ParsePoolConfig": true,
}

// connectionNames are the names the opener packages give a parameter that takes a connection
// string.
var connectionNames = map[string]bool{"connString": true, "dataSourceName": true, "dsn": true}

// configTypes are the configurations parsed from a connection string, by their qualified name
// without the package's major version.
var configTypes = []string{"pgx.ConnConfig", "pgconn.Config", "pgxpool.Config"}

// form is a function of an opener package that takes a connection string or a configuration
// parsed from one, with the index of that parameter.
type form struct {
	Callee   string `json:"callee"`
	Argument int    `json:"argument"`
	Takes    string `json:"takes"`
}

// forms reads the opener forms from the signatures of the opener packages the module imports.
func (a *analysis) readForms() {
	for importPath, pkg := range a.m.packages {
		if pkg == nil || !openerPackages.MatchString(importPath) || importPath == pgsetting || !pkg.Complete() || pkg.Scope() == nil {
			continue
		}
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			fn, ok := scope.Lookup(name).(*types.Func)
			if !ok || !fn.Exported() {
				continue
			}
			params := fn.Type().(*types.Signature).Params()
			for i := range params.Len() {
				param := params.At(i)
				if basic, ok := param.Type().Underlying().(*types.Basic); ok && basic.Kind() == types.String && connectionNames[param.Name()] {
					a.forms[funcName(fn)] = form{funcName(fn), i, "a connection string (" + param.Name() + ")"}
					break
				}
				if takes := configType(param.Type()); takes != "" {
					a.forms[funcName(fn)] = form{funcName(fn), i, "a configuration (" + takes + ")"}
					break
				}
			}
		}
	}
}

// configType is the configuration t is, or points to, by configTypes' name, or "".
func configType(t types.Type) string {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	name := named.Obj().Pkg().Name() + "." + named.Obj().Name()
	for _, c := range configTypes {
		if c == name && strings.HasPrefix(named.Obj().Pkg().Path(), "github.com/jackc/") {
			return name
		}
	}
	return ""
}

func (a *analysis) formList() []form {
	var list []form
	for _, f := range a.forms {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Callee < list[j].Callee })
	return list
}

// funcName is f as its package's path, a dot and its name, with the receiver's type name and a dot
// before a method's name.
func funcName(f *types.Func) string {
	if f.Pkg() == nil {
		return f.Name()
	}
	if recv := f.Type().(*types.Signature).Recv(); recv != nil {
		t := recv.Type()
		if pointer, ok := t.(*types.Pointer); ok {
			t = pointer.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			return f.Pkg().Path() + "." + named.Obj().Name() + "." + f.Name()
		}
		return f.Pkg().Path() + ".(method)." + f.Name()
	}
	return f.Pkg().Path() + "." + f.Name()
}
