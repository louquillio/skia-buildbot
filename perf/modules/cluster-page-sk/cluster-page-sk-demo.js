import './index.js'
import 'elements-sk/error-toast-sk'
import { fetchMock } from '@bundled-es-modules/fetch-mock';

fetchMock.post('/_/count/', async function() {
  // Wait 1s before returning the content so we can see the spinner in action.
  return await new Promise(res => setTimeout(() => res({count: Math.floor(Math.random()*2000)}), 1000))
});

fetchMock.get('/_/initpage/?tz=America/New_York', () => {
  return {
    "dataframe": {
      "traceset": null,
      "header": null,
      "paramset": {
        "arch": [
          "WASM",
          "arm",
          "arm64",
          "asmjs",
          "wasm",
          "x86",
          "x86_64"
        ],
        "bench_type": [
          "BRD",
          "deserial",
          "micro",
          "playback",
          "recording",
          "skandroidcodec",
          "skcodec",
          "tracing"
        ],
        "browser": [
          "Chrome"
        ],
        "clip": [
          "0_0_1000_1000"
        ],
        "compiled_language": [
          "asmjs",
          "wasm"
        ],
        "compiler": [
          "Clang",
          "EMCC",
          "GCC",
          "MSVC",
          "emsdk",
          "none"
        ],
        "config": [
          "8888",
          "angle_d3d11_es2",
          "angle_d3d11_es2_msaa8",
          "angle_gl_es2",
          "angle_gl_es2_msaa8",
          "commandbuffer",
          "default",
          "enarrow",
          "esrgb",
          "f16",
          "gl",
          "gles",
          "glesmsaa4",
          "glessrgb",
          "glmsaa4",
          "glmsaa8",
          "glsrgb",
          "meta",
          "mtl",
        ],
        "configuration": [
          "Debug",
          "Presubmit",
          "Release",
          "devrel",
          "eng",
          "sdk"
        ],
        "cpu_or_gpu": [
          "CPU",
          "GPU"
        ],
      },
      "skip": 0
    },
    "ticks": [],
    "skps": [],
    "msg": "",
  };
});

fetchMock.post('/_/cidRange/', () => {
  console.log('fetch /_/cidRange');
  return [
    {
      "offset":43389,
      "source":"master",
      "author":"Avinash Parchuri (aparchur@google.com)",
      "message":"3a543aa - 23h 34m - Reland \"[skottie] Add onTextProperty support into ",
      "url":"https://skia.googlesource.com/skia/+/3a543aafd4e68af182ef88572086c094cd63f0b2",
      "hash":"3a543aafd4e68af182ef88572086c094cd63f0b2",
      "ts":1565099441
    },
    {
      "offset":43390,
      "source":"master",
      "author":"Robert Phillips (robertphillips@google.com)",
      "message":"bdb0919 - 21h 15m - Use GrComputeTightCombinedBufferSize in GrMtlGpu::",
      "url":"https://skia.googlesource.com/skia/+/bdb0919dcc6a700b41492c53ecf06b40983d13d7",
      "hash":"bdb0919dcc6a700b41492c53ecf06b40983d13d7",
      "ts":1565107798
    },
    {
      "offset":43391,
      "source":"master",
      "author":"Hal Canary (halcanary@google.com)",
      "message":"e45bf6a - 20h 33m - experimental/editor: interface no longer uses stri",
      "url":"https://skia.googlesource.com/skia/+/e45bf6a603b7990f418eaf19ef0e2a2e59a9f449",
      "hash":"e45bf6a603b7990f418eaf19ef0e2a2e59a9f449",
      "ts":1565110328
    },
  ];
});


fetchMock.get('https://skia.org/loginstatus/', () => {
  return {
    "Email":"jcgregorio@google.com",
    "ID":"110642259984599645813",
    "LoginURL":"https://accounts.google.com/...",
    "IsAGoogler":true,
    "IsAdmin":true,
    "IsEditor":false,
    "IsViewer":true,
  }
});

