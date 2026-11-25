# Third-party software

The scanner invokes FFmpeg and FFprobe from PATH. Those programs are distributed separately.

The OCR support includes the following components.

| Component | Source and notices |
| --- | --- |
| gogosseract and its Tesseract WebAssembly build | [Source](https://github.com/Danlock/gogosseract), [license](GOGOSSERACT-LICENSE.txt), [Tesseract](https://github.com/tesseract-ocr/tesseract), [Leptonica](https://github.com/DanBloomberg/leptonica) |
| English Tesseract model | [tessdata_fast 4.1.0](https://github.com/tesseract-ocr/tessdata_fast/tree/4.1.0), [license](TESSDATA-LICENSE.txt) |
| Wazero | [Source](https://github.com/tetratelabs/wazero), [license](licenses/wazero-LICENSE.txt), [notice](licenses/wazero-NOTICE.txt) |
| Wazero Emscripten binding | [Source](https://github.com/jerbob92/wazero-emscripten-embind), [license](licenses/emscripten-embind-LICENSE.txt) |
| Go libraries | [Go](https://go.dev/), [Go license](licenses/go-LICENSE.txt), [x/exp license](licenses/go-exp-LICENSE.txt), [x/text license](licenses/go-text-LICENSE.txt) |

The Go module files give the exact Go dependency versions for source builds.
