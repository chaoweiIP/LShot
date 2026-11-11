import CoreGraphics
import Foundation

struct Frame {
    let image: CGImage
    let rows: [UInt8]
    let columns: Int

    init?(_ image: CGImage) {
        self.image = image
        columns = min(image.width, 64)
        guard let context = CGContext(
            data: nil, width: columns, height: image.height,
            bitsPerComponent: 8, bytesPerRow: columns,
            space: CGColorSpaceCreateDeviceGray(), bitmapInfo: CGImageAlphaInfo.none.rawValue
        ), let data = context.data else { return nil }
        context.interpolationQuality = .low
        // Scroll indicators live along the sides and move independently of the page.
        let inset = image.width / 40
        guard let matchingArea = image.cropping(to: CGRect(
            x: inset, y: 0, width: image.width - inset * 2, height: image.height
        )) else { return nil }
        context.draw(matchingArea, in: CGRect(x: 0, y: 0, width: columns, height: image.height))
        rows = Array(UnsafeBufferPointer(
            start: data.assumingMemoryBound(to: UInt8.self), count: columns * image.height
        ))
    }

    func stationaryEdges(comparedTo previous: Frame) -> (top: Int, bottom: Int) {
        let height = image.height
        let limit = height / 3

        func length(fromBottom: Bool) -> Int {
            var count = 0
            while count < limit {
                let row = fromBottom ? height - count - 1 : count
                let start = row * columns
                var difference = 0
                for index in start..<(start + columns) {
                    difference += abs(Int(rows[index]) - Int(previous.rows[index]))
                }
                if difference > columns { break }
                count += 1
            }
            return count >= 8 && count < limit ? count : 0
        }

        let suffix = length(fromBottom: true)
        guard suffix > 0 else { return (length(fromBottom: false), 0) }

        // Only skip blank rows above the footer. Its first visible detail may
        // be a small handle above the full-width bar.
        let boundary = height - suffix
        var footerTop = boundary
        for row in boundary..<min(height, boundary + min(48, suffix / 3)) {
            let index = row * columns
            var verticalChange = 0
            var horizontalChange = 0
            for x in 0..<columns {
                verticalChange += abs(Int(rows[index + x]) - Int(rows[index - columns + x]))
                if x > 0 {
                    horizontalChange += abs(Int(rows[index + x]) - Int(rows[index + x - 1]))
                }
            }
            if verticalChange >= columns || horizontalChange >= columns * 2 {
                footerTop = row
                break
            }
        }
        return (length(fromBottom: false), height - footerTop)
    }

    func advance(from previous: Frame, top: Int, bottom: Int, tolerateOverlays: Bool = false,
                 maximumAdvance: Int = .max) -> Int? {
        let end = image.height - bottom
        let height = end - top
        guard height > 2, maximumAdvance >= 0 else { return nil }

        // Sample rows with vertical detail, distributed across the overlap.
        var detail = [Int](repeating: 0, count: end)
        for y in (top + 1)..<end {
            for x in 0..<columns {
                let index = y * columns + x
                detail[y] += abs(Int(rows[index]) - Int(rows[index - columns]))
            }
        }

        var candidates: [(offset: Int, error: Double)] = []
        let searchLimit = tolerateOverlays ? height - max(96, height / 4) : height * 2 / 3
        for offset in 0...max(0, min(searchLimit, maximumAdvance)) {
            let overlapEnd = end - offset
            let overlap = height - offset
            var difference = 0
            var weight = 0
            var bandDifferences = [0, 0, 0, 0]
            var bandWeights = [0, 0, 0, 0]
            let step = max(1, overlap / 24)
            for start in stride(from: top + 1, to: overlapEnd, by: step) {
                let end = min(start + step, overlapEnd)
                var y = start
                for row in start..<end where detail[row] > detail[y] { y = row }
                let previousRow = y + offset
                let index = y * columns
                let previousIndex = previousRow * columns
                var rowDifference = 0
                var rowWeight = 0
                for x in 0..<columns {
                    let a = Int(rows[index + x])
                    let b = Int(previous.rows[previousIndex + x])
                    rowDifference += abs(a - b)
                    rowWeight += abs(a - Int(rows[index - columns + x]))
                    rowWeight += abs(b - Int(previous.rows[previousIndex - columns + x]))
                }
                difference += rowDifference
                weight += rowWeight
                if tolerateOverlays {
                    let band = (y - top) * 4 / overlap
                    bandDifferences[band] += rowDifference
                    bandWeights[band] += rowWeight
                }
            }
            if tolerateOverlays {
                // A floating control can dominate the overall error. Require
                // agreement in separated bands before verifying the candidate.
                let agreeing = bandWeights.indices.filter {
                    bandWeights[$0] >= columns * 3 &&
                    Double(bandDifferences[$0]) < 0.3 * Double(bandWeights[$0])
                }
                guard agreeing.count >= 2 else { continue }
                if let error = tileError(previous: previous, offset: offset, top: top, end: overlapEnd) {
                    candidates.append((offset, error))
                }
                continue
            }
            guard weight >= columns * 8, Double(difference) / Double(weight) < 0.3 else { continue }

            // Verify all overlap pixels to distinguish nearby offsets in flat bands.
            var fullDifference = 0
            let shift = offset * columns
            for index in (top * columns)..<(overlapEnd * columns) {
                fullDifference += abs(Int(rows[index]) - Int(previous.rows[index + shift]))
            }
            let fullDetail = detail[top..<overlapEnd].reduce(0, +)
            guard fullDetail >= columns * 8 else { continue }
            candidates.append((offset, Double(fullDifference) / Double(fullDetail)))
        }
        let maximumError = tolerateOverlays ? 0.16 : 0.22
        guard let best = candidates.min(by: { $0.error < $1.error }), best.error < maximumError else {
            return nil
        }
        if candidates.contains(where: {
            $0.offset != best.offset && $0.error <= best.error + 0.015
        }) { return nil }
        return best.offset
    }

    private func tileError(previous: Frame, offset: Int, top: Int, end: Int) -> Double? {
        var scores: [Double] = []
        var agreeingBands = 0
        for band in 0..<4 {
            let startRow = top + (end - top) * band / 4
            let endRow = top + (end - top) * (band + 1) / 4
            var agreeingTiles = 0
            for tile in 0..<4 {
                let startColumn = columns * tile / 4
                let endColumn = columns * (tile + 1) / 4
                var difference = 0
                var detail = 0
                for row in max(1, startRow)..<endRow {
                    for column in startColumn..<endColumn {
                        let index = row * columns + column
                        let value = Int(rows[index])
                        difference += abs(value - Int(previous.rows[index + offset * columns]))
                        detail += abs(value - Int(rows[index - columns]))
                    }
                }
                guard detail >= (endColumn - startColumn) * 8 else { continue }
                let score = Double(difference) / Double(detail)
                scores.append(score)
                if score < 0.22 { agreeingTiles += 1 }
            }
            if agreeingTiles >= 2 { agreeingBands += 1 }
        }
        guard agreeingBands >= 2, scores.count >= 6 else { return nil }
        let sample = scores.sorted().prefix((scores.count + 1) / 2)
        return sample.reduce(0, +) / Double(sample.count)
    }
}

struct Stitcher {
    private(set) var strips: [CGImage] = []
    private(set) var height = 0
    private var previous: Frame?
    private var matchingFooterHeight: Int?
    private var finalRows: CGImage?
    var hasScrolled: Bool { strips.count > 1 }

    enum Result { case added, unchanged, mismatch, full }

    mutating func append(_ image: CGImage) -> Result {
        guard let frame = Frame(image) else { return .mismatch }
        guard let previous else {
            guard image.height <= 65_000, image.height * image.width <= 48_000_000 else { return .full }
            strips.append(image)
            height = image.height
            self.previous = frame
            return .added
        }
        guard image.width == previous.image.width, image.height == previous.image.height else { return .mismatch }
        if frame.rows == previous.rows { return .unchanged }

        let edges = frame.stationaryEdges(comparedTo: previous)
        let bottom = matchingFooterHeight ?? edges.bottom
        let trailingHeight = max(image.height / 5, bottom + 24)
        let contentEnd = image.height - trailingHeight
        let advance = frame.advance(from: previous, top: edges.top, bottom: bottom, maximumAdvance: contentEnd)
            ?? frame.advance(from: previous, top: max(edges.top, image.height / 6), bottom: bottom,
                             maximumAdvance: contentEnd)
            ?? frame.advance(
                from: previous,
                top: max(edges.top, image.height / 5),
                bottom: max(bottom, image.height / 5),
                tolerateOverlays: true,
                maximumAdvance: contentEnd - max(edges.top, image.height / 5)
            )
        guard let advance else { return .mismatch }
        guard advance > 0 else { return .unchanged }

        let newHeight = height + advance
        guard newHeight <= 65_000, newHeight * image.width <= 48_000_000 else { return .full }
        // Keep joins above moving controls. The final frame supplies the rows
        // below the join, so a collapsing toolbar is included only at the end.
        guard contentEnd - advance >= 0 else { return .mismatch }
        guard let tail = image.cropping(to: CGRect(
            x: 0, y: contentEnd - advance, width: image.width, height: advance
        )), let ending = image.cropping(to: CGRect(
            x: 0, y: contentEnd, width: image.width, height: trailingHeight
        )) else { return .mismatch }

        // Copy the cropped pixels so each strip retains only its own buffer.
        guard let context = Self.canvas(width: tail.width, height: tail.height) else { return .full }
        context.draw(tail, in: CGRect(x: 0, y: 0, width: tail.width, height: tail.height))
        guard let copy = context.makeImage() else { return .full }

        if !hasScrolled {
            guard let first = strips[0].cropping(to: CGRect(
                x: 0, y: 0, width: image.width, height: contentEnd
            )) else { return .mismatch }
            strips[0] = first
            matchingFooterHeight = bottom
        }
        strips.append(copy)
        finalRows = ending
        height = newHeight
        self.previous = frame
        return .added
    }

    func combinedImage() -> CGImage? {
        guard let width = strips.first?.width, let context = Self.canvas(width: width, height: height)
        else { return nil }
        var top = height
        for strip in strips {
            top -= strip.height
            context.draw(strip, in: CGRect(x: 0, y: top, width: width, height: strip.height))
        }
        if let finalRows {
            context.draw(finalRows, in: CGRect(x: 0, y: 0, width: width, height: finalRows.height))
        }
        return context.makeImage()
    }

    private static func canvas(width: Int, height: Int) -> CGContext? {
        CGContext(
            data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
            space: CGColorSpace(name: CGColorSpace.sRGB)!,
            bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue
        )
    }
}
