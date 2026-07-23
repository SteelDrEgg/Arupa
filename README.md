<div align="center">

<br>
<img width="40%" src="doc/Arupa-full-white.svg" alt="Arupa service page" />

**An app hosting platform for server**

[![Release](https://github.com/SteelDrEgg/Arupa/actions/workflows/release.yml/badge.svg?branch=main)](https://github.com/SteelDrEgg/Arupa/actions/workflows/release.yml)
[![CI](https://github.com/SteelDrEgg/Arupa/actions/workflows/ci.yml/badge.svg)](https://github.com/SteelDrEgg/Arupa/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)
[![Stars](https://img.shields.io/github/stars/SteelDrEgg/Arupa?style=flat-plastic)](https://github.com/SteelDrEgg/Arupa/stargazers)
![License](https://img.shields.io/badge/license-MIT-maroon)

</div>

---

Start from [here](https://docs.arupa.dev/).

The kernel loads `.plg` service packages with one of three runtimes: `static`,
`wasm`, or `grpc`. Services dynamically register `static`, `http`,
`socket.io`, and `proxy` transports, then bind routes to those transports.
See [the service architecture](docs/service-architecture.md) for the v2
contract and routing rules.
