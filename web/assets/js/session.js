;(function (global) {
  const SOCKET_IO_CDN = "https://cdn.socket.io/4.7.5/socket.io.min.js"
  const DEFAULT_SOCKET_PATH = "/socket.io/"
  const DEFAULT_USER_API = "/api/session/me"

  let socketIOLoadPromise = null

  function loadScript(src) {
    if (global.io) return Promise.resolve(global.io)
    if (socketIOLoadPromise) return socketIOLoadPromise

    socketIOLoadPromise = new Promise(function (resolve, reject) {
      const script = document.createElement("script")
      script.src = src || SOCKET_IO_CDN
      script.async = true
      script.onload = function () {
        if (!global.io) {
          reject(new Error("Socket.IO loaded but io is unavailable"))
          return
        }
        resolve(global.io)
      }
      script.onerror = function () {
        reject(new Error("Failed to load Socket.IO"))
      }
      document.head.appendChild(script)
    })

    return socketIOLoadPromise
  }

  async function getCurrentUsername(apiPath) {
    const response = await fetch(apiPath || DEFAULT_USER_API, {
      method: "GET",
      credentials: "include"
    })

    if (!response.ok) {
      throw new Error("Failed to fetch current username")
    }

    const payload = await response.json()
    if (payload && payload.data && typeof payload.data.username === "string") {
      return payload.data.username
    }

    // Backward compatible fallback for old response shape.
    return payload.username || ""
  }

  async function createSocket(namespace) {
    const ioFactory = await loadScript(SOCKET_IO_CDN)
    return ioFactory(namespace || "/", {
      path: DEFAULT_SOCKET_PATH
    })
  }

  global.Session = {
    getCurrentUsername: getCurrentUsername,
    createSocket: createSocket
  }
})(window)
