//  Copyright (c) 2012-2013 Couchbase, Inc.
//  Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
//  except in compliance with the License. You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
//  Unless required by applicable law or agreed to in writing, software distributed under the
//  License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
//  either express or implied. See the License for the specific language governing permissions
//  and limitations under the License.

package channels

import (
	"fmt"
	"strconv"

	"github.com/couchbaselabs/walrus"
	"github.com/robertkrimen/otto"

	"github.com/couchbaselabs/sync_gateway/base"
)

const funcWrapper = `
	function(newDoc, oldDoc, userCtx) {
		var v = %s;
		try {
			v(newDoc, oldDoc, userCtx);
		} catch(x) {
			if (x.forbidden)
				reject(403, x.forbidden);
			else if (x.unauthorized)
				reject(401, x.unauthorized);
			else
				reject(500, String(x));
		}
	}`

/** Result of running a channel-mapper function. */
type ChannelMapperOutput struct {
	Channels  []string
	Access    AccessMap
	Rejection *base.HTTPError
}

type ChannelMapper struct {
	output *ChannelMapperOutput
	js     *walrus.JSServer
}

// Maps user names to arrays of channel names
type AccessMap map[string][]string

// Converts a JS array into a Go string array.
func ottoArrayToStrings(array *otto.Object) []string {
	lengthVal, err := array.Get("length")
	if err != nil {
		return nil
	}
	length, err := lengthVal.ToInteger()
	if err != nil || length <= 0 {
		return nil
	}

	result := make([]string, 0, length)
	for i := 0; i < int(length); i++ {
		item, err := array.Get(strconv.Itoa(i))
		if err == nil && item.IsString() {
			result = append(result, item.String())
		}
	}
	return result
}

func NewChannelMapper(funcSource string) (*ChannelMapper, error) {
	funcSource = fmt.Sprintf(funcWrapper, funcSource)
	mapper := &ChannelMapper{}
	var err error
	mapper.js, err = walrus.NewJSServer(funcSource)
	if err != nil {
		return nil, err
	}

	// Implementation of the 'channel()' callback:
	mapper.js.DefineNativeFunction("channel", func(call otto.FunctionCall) otto.Value {
		for _, arg := range call.ArgumentList {
			if arg.IsString() {
				mapper.output.Channels = append(mapper.output.Channels, arg.String())
			} else if arg.Class() == "Array" {
				array := ottoArrayToStrings(arg.Object())
				if array != nil {
					mapper.output.Channels = append(mapper.output.Channels, array...)
				}
			}
		}
		return otto.UndefinedValue()
	})

	// Implementation of the 'access()' callback:
	mapper.js.DefineNativeFunction("access", func(call otto.FunctionCall) otto.Value {
		username := call.Argument(0)
		channels := call.Argument(1)
		usernameArray := []string{}
		if username.IsString() {
			usernameArray = []string{username.String()}
		} else if username.Class() == "Array" {
			usernameArray = ottoArrayToStrings(username.Object())
		}
		for _, name := range usernameArray {
			if channels.IsString() {
				mapper.output.Access[name] = append(mapper.output.Access[name], channels.String())
			} else if channels.Class() == "Array" {
				array := ottoArrayToStrings(channels.Object())
				if array != nil {
					mapper.output.Access[name] = append(mapper.output.Access[name], array...)
				}
			}
		}
		return otto.UndefinedValue()
	})

	// Implementation of the 'reject()' callback:
	mapper.js.DefineNativeFunction("reject", func(call otto.FunctionCall) otto.Value {
		if mapper.output.Rejection == nil {
			if status, err := call.Argument(0).ToInteger(); err == nil && status >= 400 {
				var message string
				if len(call.ArgumentList) > 1 {
					message = call.Argument(1).String()
				}
				mapper.output.Rejection = &base.HTTPError{int(status), message}
			}
		}
		return otto.UndefinedValue()
	})

	mapper.js.Before = func() {
		mapper.output = &ChannelMapperOutput{
			Channels: []string{},
			Access:   map[string][]string{},
		}
	}
	mapper.js.After = func(result otto.Value, err error) (interface{}, error) {
		output := mapper.output
		mapper.output = nil
		output.Channels = SimplifyChannels(output.Channels, false)
		return output, err
	}
	return mapper, nil
}

func NewDefaultChannelMapper() (*ChannelMapper, error) {
	return NewChannelMapper(`function(doc){channel(doc.channels);}`)
}

// This is just for testing
func (mapper *ChannelMapper) callMapper(body string, oldBody string, userCtx string) (*ChannelMapperOutput, error) {
	res, err := mapper.js.DirectCallFunction([]string{body, oldBody, userCtx})
	return res.(*ChannelMapperOutput), err
}

func (mapper *ChannelMapper) MapToChannelsAndAccess(body string, oldBody string, userCtx string) (*ChannelMapperOutput, error) {
	result1, err := mapper.js.CallFunction([]string{body, oldBody, userCtx})
	if err != nil {
		return nil, err
	}
	output := result1.(*ChannelMapperOutput)
	return output, nil
}

func (mapper *ChannelMapper) SetFunction(fnSource string) (bool, error) {
	return mapper.js.SetFunction(fnSource)
}

func (mapper *ChannelMapper) Stop() {
	mapper.js.Stop()
}
