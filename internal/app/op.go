package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/imohiyoko/API/define"
)

// Op は登録できる操作。Command / Query だけが作る(どの操作も Guard を通す。ADR 0008 の決定 2)。
type Op struct {
	def     define.Operation
	guarded bool
}

// Name は操作の名前。
func (o Op) Name() string {
	if o.def == nil {
		return ""
	}
	s, _ := o.def.Spec()
	return s.Name
}

// Command は更新の操作を、門 gate で包んで定義する。
func Command[In, Out any](env *Env, name string, gate Gate, h func(context.Context, In) (Out, error), opts ...define.Option) Op {
	return Op{def: define.Command[In, Out](name, opts...).Handle(guard(env, gate, h)), guarded: true}
}

// Query は読み取りの操作を、門 gate で包んで定義する。
func Query[In, Out any](env *Env, name string, gate Gate, h func(context.Context, In) (Out, error), opts ...define.Option) Op {
	return Op{def: define.Query[In, Out](name, opts...).Handle(guard(env, gate, h)), guarded: true}
}

// Registry は操作を集める。Guard を通さない操作(Op のゼロ値など)があれば error。
func Registry(ops ...Op) (*define.Registry, error) {
	reg := define.NewRegistry(define.WithName("herdr"))
	var errs []error
	for i, o := range ops {
		if o.def == nil || !o.guarded {
			errs = append(errs, fmt.Errorf("app: operation #%d (%q) is not wrapped by Guard", i, o.Name()))
			continue
		}
		reg.Add(o.def)
	}
	return reg, errors.Join(errs...)
}
