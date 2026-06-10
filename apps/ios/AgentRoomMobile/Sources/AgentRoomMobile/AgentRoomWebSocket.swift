import Foundation

public final class AgentRoomWebSocket: Sendable {
    private let session: URLSession
    private let url: URL
    private let taskLock = NSLock()
    private nonisolated(unsafe) var task: URLSessionWebSocketTask?

    public init(url: URL, session: URLSession = .shared) {
        self.url = url
        self.session = session
    }

    public func connect() {
        taskLock.lock()
        defer { taskLock.unlock() }
        guard task == nil else { return }
        let nextTask = session.webSocketTask(with: url)
        task = nextTask
        nextTask.resume()
    }

    public func disconnect() {
        taskLock.lock()
        let current = task
        task = nil
        taskLock.unlock()
        current?.cancel(with: .goingAway, reason: nil)
    }

    public func messages() -> AsyncThrowingStream<AgentRoomMessage, Error> {
        connect()
        return AsyncThrowingStream { continuation in
            Task {
                do {
                    while !Task.isCancelled {
                        guard let task = currentTask() else { break }
                        let message = try await task.receive()
                        guard case .string(let text) = message else { continue }
                        let data = Data(text.utf8)
                        let decoded = try AgentRoomCoding.decoder.decode(AgentRoomMessage.self, from: data)
                        continuation.yield(decoded)
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
        }
    }

    public func send(_ message: AgentRoomOutgoingMessage) async throws {
        connect()
        guard let task = currentTask() else { return }
        let data = try AgentRoomCoding.encoder.encode(message)
        let text = String(decoding: data, as: UTF8.self)
        try await task.send(.string(text))
    }

    private func currentTask() -> URLSessionWebSocketTask? {
        taskLock.lock()
        defer { taskLock.unlock() }
        return task
    }
}
