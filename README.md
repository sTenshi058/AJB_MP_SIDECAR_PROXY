# AJB_MP_SIDECAR_PROXY

Standalone Go sidecar for connecting AJB PC Port UDP gameplay through EOS P2P.
It does not modify the game port.
Details TBA.

The EOS headers are supplied by the external EOS SDK, not this repository.

AI assistance has been used for debugging purposes and testing.

## Windows release dependencies

The Windows build statically links the MinGW GCC and C++ runtime libraries,
including libstdc++, and the distribution package includes
`EOSSDK-Win64-Shipping.dll`. Testers should not need to separately install or
download the MinGW runtime or EOS SDK. Normal Windows system components remain
required. This depends on the MinGW toolchain providing static runtime
libraries; verify the release executable on a clean Windows machine before
publishing it.

## Server-browser and gameplay test

The sidecar listens on UDP/14001 while hosting or joined to an EOS lobby. It
recognizes the captured `SQ` LAN query, echoes its token in an `SR` response,
and advertises `127.0.0.1` at the local UDP bridge port. The reply uses the
observed response layout and capture-derived fixed fields; the capture was
from one LAN setup, so compatibility with the game's live browser still needs
to be confirmed.

1. On the host, start AJB as a listen server on UDP/1170, then run
   `./dist/sidecar host`.
2. Give the printed EOS lobby ID to the other player.
3. On the other player's computer, run `./dist/sidecar join LOBBY_ID` and
   leave it running.
4. In AJB, search for LAN games. The sidecar should log each recognized query
   and its response, and the browser should show `AJB Sidecar Relay`.
5. Select that entry to test the connection through EOS. If the browser does
   not join successfully, the direct-connect fallback is
   `open 127.0.0.1:1170` in the game's Unreal console.

Unrecognized UDP/14001 packets are logged but not answered. Both players must
keep their sidecars running throughout the match.

On the first gameplay attempt, each sidecar logs the first UDP/1170 packet it
forwards in each direction. If a match fails, those messages help distinguish
browser discovery from actual gameplay traffic through EOS.
