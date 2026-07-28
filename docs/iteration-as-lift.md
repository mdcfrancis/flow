# Iteration as lift — one array abstraction for compute and render

## The problem the nebula surfaced

A creative grow chose to build a particle nebula: particles in an array, pulled
toward an attractor, drawn as a field of circles. Physics converged (it hand-wrote a
WAT loop over the array). **The renderer never moved off the scaffold placeholder** —
every attempt failed to expand with:

```
macro-WAT did not expand: unknown draw prim "loop" (want circle/rect/line)
```

The model's translation was *correct*: it wrote `(scene (loop … (circle (atidx …) …)))`
— draw one circle per particle. But `(scene PRIM…)` is a **closed, compile-time list of
primitives**. It cannot iterate an array. So the correct pseudocode had nowhere to land.

The narrow read is "add a loop to `scene`." The real read is deeper.

## The chain of reframes

1. **Accumulator.** The render target is a **z-ordered stream of 24-byte records**;
   drawing is just *append the next record* (paint order = z order). `(scene)` is only a
   fixed batch of appends. What's missing is *append as a statement usable anywhere*.
2. **Iteration.** To append per particle you need iteration — and physics proves raw WAT
   loops work. So the gap is a *structured* iterator plus an append usable *inside* it.
3. **Dispatch (Julia-style).** The expander already knows every field's type from the
   `Layout` (`TInt`/`TFloat` = singleton, `TBuffer{Len}` = array). So the *same*
   expression can route by argument kind: `(draw (circle X Y R C))` with scalar `X,Y` →
   one record; with array `X,Y` → iterate. Iteration becomes an emergent property of
   applying a scalar op to an array.
4. **We already have the list abstraction.** `map`/`iterate`/`fold`/`zip` (the combinator
   cells in `stdlib/cells/`) *already* iterate arrays via cell-dispatch. A new `(for)` in
   the macro layer would reinvent iteration we already have. The renderer isn't a loop —
   it's a **list transformation**: `draw_stream = map(particle → draw_record)`.
5. **Struct-valued map.** Our `map` writes *one i32* per element (`out[i] = leaf(in[i])`).
   A draw record is a **struct — 6 i32s** (op, a, b, c, d, rgba). So the gap is not "how
   do we iterate," it's "map produces scalars, a particle maps to a *record*." We need a
   **record-valued map**, and the render ABI reads its output as the draw stream.
6. **Scalar = length-1 array ⇒ it's a lift.** The final collapse: stop dispatching
   between scalar and array. **A scalar is an array of length 1.** Then there is exactly
   one operation — *map over an array* — and the scalar case is the degenerate length-1
   case. Every element-wise op is *lifted* to run over arrays; a "scalar op" is that same
   op at length 1. The scalar/array split dissolves.

## The unified model

- **Everything is an array. A scalar is a length-1 array.** Fields carry a length
  (scalars: 1; buffers: `Len`). There is no scalar/array dispatch — only lengths.
- **Every element-wise operation is a LIFT.** Define an op on *one element*; it runs over
  an array by mapping. `add`, `set`, `draw-one` — all defined per element, lifted to
  arrays for free. A user `defmacro` written for one element automatically applies to an
  array.
- **A render cell is a struct-valued `map`.** `map(particle → record)` over the particle
  array *is* the renderer; its output *is* the z-ordered draw stream. A static scene is
  the length-1/length-k case of the same map — `(scene)` becomes sugar for a
  fixed-length map.
- **Compute and render share one mechanism.** Physics = `map`/`zip`/`fold` over the
  particle arrays writing width-1 outputs. Render = the same `map` writing width-6
  (record) outputs. One iteration abstraction, two output widths.

```
draw_stream = map6(draw_one, particles)      ; width-6 map = record per element
new_pos     = zip(add, pos, vel)             ; width-1 map = i32 per element
one record  = map6(draw_one, [particle])     ; scalar draw = length-1 case
```

## What actually has to change

The abstraction (`map`) exists. Three impedance matches remain:

1. **Struct/width outputs in `map`.** Today `map` writes one i32 per element. It needs a
   per-element output **width `W`** (words), and a leaf ABI that *writes* `W` words rather
   than *returning* one i32 — e.g. `leaf(inPtr, inLen, outPtr)` writes the element's
   record. `W=1` is today's map; `W=6` is a draw record. (`stdlib/cells/map.wat` +
   `stdlib/templates/map-driver.wat.tmpl` are where this lands — the map driver already
   carries `elemWords`; it needs an `outWords` too.)
2. **Render ABI = a width-6 map's output.** A render cell is compiled as: invoke the
   width-6 `map` with a per-particle draw leaf; its output buffer *is* the draw stream the
   canvas reads (records already in array order = z order). `(scene …)` lowers to the
   length-k case.
3. **Lift + length in the surface.** A field's length is known from the `Layout`
   (scalar = 1, buffer = `Len`). An element-wise macro/op is defined once and lifted; the
   length drives the map bound. No `::scalar`/`::array` methods needed — just length.

## Why this is worth the ABI change

- It removes a whole category of "the surface can't express this" stalls: any
  array-backed visual (particles, tiles, sprites, a grid) is `map(element → record)`.
- It **unifies compute and render** under one iteration primitive instead of a bespoke
  draw loop — less surface, one thing to get right, one thing to optimize (a fused map).
- It's the honest generalization the run demanded: the model already writes the correct
  per-element pseudocode; lift makes that *be* the program.

## Phasing (each independently landable + verifiable)

1. **Width-`W` map ABI.** Extend `map` (cell + driver template) to a per-element output
   width and a write-style leaf. Verify `W=1` unchanged; `W=6` emits records that
   assemble + run. *(Pure runtime/combinator work — no surface change yet.)*
2. **Render = width-6 map.** Compile a render cell as a width-6 map over its array input
   with a draw-one leaf; the output is the draw stream. Keep `(scene …)` working as the
   fixed-length case. Verify the bounce/nebula renderers draw correctly.
3. **Lift in the surface.** Length-driven lifting so an element-wise op/macro applies to
   an array with no dispatch; scalar = length-1. Verify a single `defmacro` runs over both
   a scalar and an array field.

## The pragmatic escape hatch (orthogonal)

None of the above blocks the nebula *today*: the model can already hand-write
`(for)`-style iteration in raw WAT (physics does). If we want the nebula to render
*before* the ABI work lands, the minimal unblock is a `(draw PRIM)` append-statement so a
raw WAT loop can emit records — but that is the *redundant* second iterator this design
exists to avoid. Preference: build the lift/map path, not the parallel loop.
