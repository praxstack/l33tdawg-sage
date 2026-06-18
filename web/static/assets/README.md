# MRI brain mesh

The 3D MRI view (`/ui/mri` and the in-dashboard MRI toggle) renders the memory
cloud inside a brain-shaped wireframe.

## `brain.obj` — bundled anatomical mesh

`brain.obj` is an anatomical brain mesh (cerebrum + brainstem) rendered as a
glowing wireframe hull. It is a **third-party asset under CC BY 4.0** — *not*
Apache-2.0. Attribution is required and recorded in the repo-root
[`THIRD_PARTY_NOTICES.md`](../../../THIRD_PARTY_NOTICES.md):

> "Human brain, Cerebrum & Brainstem" (https://skfb.ly/6SDzJ) by FrankJohansson
> is licensed under Creative Commons Attribution (https://creativecommons.org/licenses/by/4.0/).

It was exported from the original `.glb` to geometry-only Wavefront OBJ
(positions + faces; textures/materials stripped, node transforms baked).

## Fallback

If `brain.obj` is absent (or not a valid mesh), the view falls back to a
**procedurally-generated** hull (`makeBrainGeometry()` in `../js/mri-brain.js`) —
license-free and zero-dependency.

## Replacing it

Drop your own `brain.obj` here (Wavefront OBJ; positions + faces; parsed inline,
no loader lib). Centred near origin, any scale (auto-normalised). Prefer
low-to-medium poly (≤ ~50k faces) for a clean wireframe. If you swap it, update
`THIRD_PARTY_NOTICES.md` to match — and prefer **CC0 / public-domain** or
**CC BY** (attribution-only); avoid CC BY-SA (copyleft), CC BY-NC (no commercial
use), and CC BY-ND (no modifications).
