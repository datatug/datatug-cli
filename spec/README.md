# datatug-cli specification

This directory contains the specification for the `datatug` CLI — the command-line agent for the [DataTug](https://datatug.app) data exploration platform.

The specification format follows [SpecScore](https://specscore.md). This tree specifies only the CLI's contract — its commands, flags, output, exit codes, and behavior. The underlying project format (the on-disk DataTug project files the CLI reads and writes) is a separate concern.

## Structure

```
spec/
└── features/
    └── cli/        # The datatug CLI feature tree
```

See [features/](features/README.md).

## Open Questions

None at this time.
