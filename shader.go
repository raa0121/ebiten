// Copyright 2020 The Ebiten Authors
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

package ebiten

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"strings"

	"github.com/hajimehoshi/ebiten/internal/graphics"
	"github.com/hajimehoshi/ebiten/internal/mipmap"
	"github.com/hajimehoshi/ebiten/internal/shader"
	"github.com/hajimehoshi/ebiten/internal/shaderir"
)

var shaderSuffix string

func init() {
	shaderSuffix = `
var __imageDstTextureSize vec2

// imageSrcTextureSize returns the destination image's texture size in pixels.
func imageDstTextureSize() vec2 {
	return __imageDstTextureSize
}
`

	shaderSuffix += fmt.Sprintf(`
var __textureSizes [%[1]d]vec2

// imageSrcTextureSize returns the source image's texture size in pixels.
// As an image is a part of internal texture, the texture is usually bigger than the image.
// The texture's size is useful when you want to calculate pixels from texels.
func imageSrcTextureSize() vec2 {
	return __textureSizes[0]
}

// The unit is the source texture's texel.
var __textureSourceOffsets [%[2]d]vec2

// The unit is the source texture's texel.
var __textureSourceRegionOrigin vec2

// The unit is the source texture's texel.
var __textureSourceRegionSize vec2

// imageSrcTextureRegion returns the source image's region (the origin and the size) on its texture.
// The unit is the source texture's texel.
//
// As an image is a part of internal texture, the image can be located at an arbitrary position on the texture.
func imageSrcTextureRegion() (vec2, vec2) {
	return __textureSourceRegionOrigin, __textureSourceRegionSize
}
`, graphics.ShaderImageNum, graphics.ShaderImageNum-1)

	for i := 0; i < graphics.ShaderImageNum; i++ {
		pos := "pos"
		if i >= 1 {
			// Convert the position in texture0's texels to the target texture texels.
			pos = fmt.Sprintf("(pos + __textureSourceOffsets[%d]) * __textureSizes[0] / __textureSizes[%d]", i-1, i)
		}
		// __t%d is a special variable for a texture variable.
		shaderSuffix += fmt.Sprintf(`
func imageSrc%[1]dAt(pos vec2) vec4 {
	// pos is the position in texels of the source texture (= 0th image's texture).
	return texture2D(__t%[1]d, %[2]s)
}

func imageSrc%[1]dBoundsAt(pos vec2) vec4 {
	// pos is the position in texels of the source texture (= 0th image's texture).
	return texture2D(__t%[1]d, %[2]s) *
		step(__textureSourceRegionOrigin.x, pos.x) *
		(1 - step(__textureSourceRegionOrigin.x + __textureSourceRegionSize.x, pos.x)) *
		step(__textureSourceRegionOrigin.y, pos.y) *
		(1 - step(__textureSourceRegionOrigin.y + __textureSourceRegionSize.y, pos.y))
}
`, i, pos)
	}

	shaderSuffix += `
func __vertex(position vec2, texCoord vec2, color vec4) (vec4, vec2, vec4) {
	return mat4(
		2/imageDstTextureSize().x, 0, 0, 0,
		0, 2/imageDstTextureSize().y, 0, 0,
		0, 0, 1, 0,
		-1, -1, 0, 1,
	) * vec4(position, 0, 1), texCoord, color
}
`
}

type Shader struct {
	shader       *mipmap.Shader
	uniformNames []string
	uniformTypes []shaderir.Type
}

func NewShader(src []byte) (*Shader, error) {
	var buf bytes.Buffer
	buf.Write(src)
	buf.WriteString(shaderSuffix)

	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "", buf.Bytes(), parser.AllErrors)
	if err != nil {
		return nil, err
	}

	const (
		vert = "__vertex"
		frag = "Fragment"
	)
	s, err := shader.Compile(fs, f, vert, frag, graphics.ShaderImageNum)
	if err != nil {
		return nil, err
	}

	if s.VertexFunc.Block == nil {
		return nil, fmt.Errorf("ebiten: vertex shader entry point '%s' is missing", vert)
	}
	if s.FragmentFunc.Block == nil {
		return nil, fmt.Errorf("ebiten: fragment shader entry point '%s' is missing", frag)
	}

	return &Shader{
		shader:       mipmap.NewShader(s),
		uniformNames: s.UniformNames,
		uniformTypes: s.Uniforms,
	}, nil
}

func (s *Shader) Dispose() {
	s.shader.MarkDisposed()
	s.shader = nil
}

func (s *Shader) convertUniforms(uniforms map[string]interface{}) []interface{} {
	names := map[string]int{}
	var idx int
	for _, n := range s.uniformNames {
		if strings.HasPrefix(n, "__") {
			continue
		}
		names[n] = idx
		idx++
	}

	us := make([]interface{}, len(names))
	for name, idx := range names {
		if v, ok := uniforms[name]; ok {
			// TODO: Check the uniform variable types?
			us[idx] = v
			continue
		}

		t := s.uniformTypes[idx]
		v := zeroUniformValue(t)
		if v == nil {
			panic(fmt.Sprintf("ebiten: unexpected uniform variable type: %s", t.String()))
		}
		us[idx] = v
	}

	// TODO: Panic if uniforms include an invalid name

	return us
}

func zeroUniformValue(t shaderir.Type) interface{} {
	switch t.Main {
	case shaderir.Bool:
		return false
	case shaderir.Int:
		return 0
	case shaderir.Float:
		return float32(0)
	case shaderir.Vec2:
		return make([]float32, 2)
	case shaderir.Vec3:
		return make([]float32, 3)
	case shaderir.Vec4:
		return make([]float32, 4)
	case shaderir.Mat2:
		return make([]float32, 4)
	case shaderir.Mat3:
		return make([]float32, 9)
	case shaderir.Mat4:
		return make([]float32, 16)
	case shaderir.Array:
		switch t.Sub[0].Main {
		case shaderir.Bool:
			return make([]bool, t.Length)
		case shaderir.Int:
			return make([]int, t.Length)
		case shaderir.Float:
			return make([]float32, t.Length)
		case shaderir.Vec2:
			return make([]float32, t.Length*2)
		case shaderir.Vec3:
			return make([]float32, t.Length*3)
		case shaderir.Vec4:
			return make([]float32, t.Length*4)
		case shaderir.Mat2:
			return make([]float32, t.Length*4)
		case shaderir.Mat3:
			return make([]float32, t.Length*9)
		case shaderir.Mat4:
			return make([]float32, t.Length*16)
		}
	}
	return nil
}
