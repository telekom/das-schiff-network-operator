package types

import "reflect"

func GetName(obj interface{}) string {
	t := reflect.TypeOf(obj)
	k := t.Kind()
	if k == reflect.Chan || k == reflect.Map || k == reflect.Ptr || k == reflect.Slice {
		return t.Elem().Name()
	}
	return t.Name()
}
