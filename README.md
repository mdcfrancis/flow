# HDM — Homeostatic Dataflow Machine

This is an experiment. It's a runtime that takes a plain-English objective and grows
a working program out of small WebAssembly "cells", then keeps rewriting them to be
more correct and cheaper — a language model does the writing, the runtime does the
grading.

The cells coordinate through shared memory as a dataflow. Each one declares what it
reads and writes, gets checked against small acceptance tests every cycle, and splits
itself into simpler pieces when it can't do the job in one pass. "Homeostatic" is the
honest part of the name: the whole point is keeping that dataflow correct as it
mutates, through a feedback loop of checks and evolution.

## ⚠️ It's an experiment — don't use it for anything real

**This is not production software and isn't ready for any production use case.**

- It's an active prototype. Interfaces, data formats, and on-disk state change without
  notice or migrations.
- It drives a language model to write and run code on its own. The output is
  non-deterministic, often wrong, and shouldn't be trusted without review.
- Nothing here is security-hardened, audited, or tested for reliability or data
  integrity. Don't point it at data, systems, or credentials you care about.
- No warranty — see [LICENSE](LICENSE). Use it at your own risk.

Treat it as a sketch to learn from, nothing more.

## Contributing

Conventions and the repo layout are in [CONTRIBUTING.md](CONTRIBUTING.md). The short
version: no new dependencies, cells stay deterministic, and everything builds, vets,
and tests green before commit.

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026 Michael Francis.
