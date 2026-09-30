# README for [DataTug](https://github.com/datatug/datatug) developers

## JetBrain IDEs - GoLand

To run and debug the DataTug CLI application in your run configurations,
set the `Eliminate terminal in output console` to true. This is required by the Bubble Tea terminal UI (`charm.land/bubbletea/v2`).

## Terminal UI architecture

`datatug ui` (and bare `datatug`) is a Bubble Tea v2 program built on the shared
[`strongo-tui`](https://github.com/strongo/strongo-tui) toolkit:

- **Shell** (`pkg/nav`): hosts pages, breadcrumbs, the shared menu, alerts and
  focus; the DataTug layer is `apps/datatugapp/datatugui` (modules and screens
  such as `dtviewers`, `dtproject`, `dtsettings`, `dtapiservice`).
- **Widgets** (`pkg/widgets`, `pkg/highlight`, `pkg/theme`): lists, trees, forms,
  text panes and the theme; screens never build colour literals.
- **Grid** (`pkg/grid`): every table, with row and cell selection messages.
- **Tests** (`pkg/nav/navtest`, `pkg/uitest`): drive the shell and screens
  without a terminal.

How to write and test a screen: [tui-screens.md](tui-screens.md).

## Project structure

- [apps](../apps) – contains the logic of DataTug and other mini-apps
    - [datatug](../apps/datatugapp) – defines `datatug` CLI commands & modules

## Contributing

We welcome contributions to DataTug! Please read our [contributing guidelines](CONTRIBUTING.md) for more information on
how to contribute to the project.