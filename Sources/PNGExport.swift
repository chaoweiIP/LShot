import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers
import zlib

enum PNGExport {
    static func encode(_ image: CGImage) throws -> Data {
        guard let data = CFDataCreateMutable(nil, 0),
              let destination = CGImageDestinationCreateWithData(
                  data, UTType.png.identifier as CFString, 1, nil
              ) else { throw CocoaError(.fileWriteUnknown) }
        CGImageDestinationAddImage(destination, image, nil)
        guard CGImageDestinationFinalize(destination) else {
            throw CocoaError(.fileWriteUnknown)
        }
        let original = data as Data
        return recompress(original, width: image.width, height: image.height) ?? original
    }

    private static func recompress(_ png: Data, width: Int, height: Int) -> Data? {
        // ImageIO writes opaque 8-bit RGB. Preserve the filtered scanlines
        // verbatim and change only the deflate stream in the IDAT chunks.
        guard png.count >= 33, png[24] == 8, png[25] == 2, png[28] == 0 else { return nil }
        let idat = Data("IDAT".utf8)
        var payload = Data()
        var imageChunks: [Range<Int>] = []
        var offset = 8
        while offset <= png.count - 12 {
            let size = png[offset..<(offset + 4)].reduce(0) { ($0 << 8) | Int($1) }
            guard size <= png.count - offset - 12 else { return nil }
            let end = offset + size + 12
            if png[(offset + 4)..<(offset + 8)].elementsEqual(idat) {
                imageChunks.append(offset..<end)
                payload.append(png[(offset + 8)..<(end - 4)])
            }
            offset = end
        }
        guard let first = imageChunks.first, let last = imageChunks.last,
              !payload.isEmpty, offset == png.count else { return nil }

        var rows = Data(count: height * (width * 3 + 1))
        var rowSize = uLongf(rows.count)
        let decoded = rows.withUnsafeMutableBytes { output in
            payload.withUnsafeBytes { input in
                uncompress(output.bindMemory(to: Bytef.self).baseAddress, &rowSize,
                           input.bindMemory(to: Bytef.self).baseAddress, uLong(payload.count))
            }
        }
        guard decoded == Z_OK, rowSize == rows.count else { return nil }

        var compressed = Data(count: payload.count)
        var size = uLongf(compressed.count)
        let encoded = compressed.withUnsafeMutableBytes { output in
            rows.withUnsafeBytes { input in
                compress2(output.bindMemory(to: Bytef.self).baseAddress, &size,
                          input.bindMemory(to: Bytef.self).baseAddress, rowSize, Z_BEST_COMPRESSION)
            }
        }
        guard encoded == Z_OK, size < payload.count else { return nil }
        compressed.count = Int(size)

        var chunk = idat
        chunk.append(compressed)
        let checksum = chunk.withUnsafeBytes {
            crc32(0, $0.bindMemory(to: Bytef.self).baseAddress, uInt(chunk.count))
        }
        var length = UInt32(size).bigEndian
        var crc = UInt32(checksum).bigEndian
        var result = Data(png.prefix(first.lowerBound))
        withUnsafeBytes(of: &length) { result.append(contentsOf: $0) }
        result.append(chunk)
        withUnsafeBytes(of: &crc) { result.append(contentsOf: $0) }
        result.append(png.suffix(from: last.upperBound))
        return result
    }
}
