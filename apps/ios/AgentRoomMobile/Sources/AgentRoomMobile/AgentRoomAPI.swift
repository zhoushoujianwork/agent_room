import Foundation

public struct AgentRoomAPI: Sendable {
    public enum APIError: Error, LocalizedError, Sendable {
        case invalidResponse
        case httpStatus(Int, String)

        public var errorDescription: String? {
            switch self {
            case .invalidResponse:
                return "The relay returned an invalid response."
            case .httpStatus(let status, let body):
                return "Relay request failed with HTTP \(status): \(body)"
            }
        }
    }

    public let relayBaseURL: URL
    private let session: URLSession

    public init(relayBaseURL: URL, session: URLSession = .shared) {
        self.relayBaseURL = relayBaseURL
        self.session = session
    }

    public func createRoom() async throws -> AgentRoom {
        try await send(path: "/v1/rooms", method: "POST")
    }

    public func me() async throws -> AgentRoomMe {
        try await send(path: "/v1/me", method: "GET")
    }

    public func room(id: String) async throws -> AgentRoom {
        try await send(path: "/v1/rooms/\(id)", method: "GET")
    }

    public func messages(roomID: String, limit: Int = 80) async throws -> [AgentRoomMessage] {
        try await send(path: "/v1/rooms/\(roomID)/messages?limit=\(limit)", method: "GET")
    }

    public func participants(roomID: String) async throws -> [AgentRoomParticipant] {
        try await send(path: "/v1/rooms/\(roomID)/participants", method: "GET")
    }

    @discardableResult
    public func postMessage(_ message: AgentRoomOutgoingMessage) async throws -> AgentRoomMessage {
        let body = try AgentRoomCoding.encoder.encode(message)
        return try await send(
            path: "/v1/rooms/\(message.roomID)/messages",
            method: "POST",
            body: body
        )
    }

    public func webSocketURL(roomID: String, identity: AgentRoomClientIdentity) -> URL {
        var components = URLComponents(url: relayBaseURL, resolvingAgainstBaseURL: false)!
        components.scheme = relayBaseURL.scheme == "https" ? "wss" : "ws"
        components.path = "/v1/rooms/\(roomID)/ws"
        components.queryItems = identity.queryItems
        return components.url!
    }

    private func send<T: Decodable>(
        path: String,
        method: String,
        body: Data? = nil
    ) async throws -> T {
        let url = URL(string: path, relativeTo: relayBaseURL)!
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.httpBody = body
        if body != nil {
            request.setValue("application/json", forHTTPHeaderField: "content-type")
        }

        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.httpStatus(http.statusCode, String(data: data, encoding: .utf8) ?? "")
        }
        return try AgentRoomCoding.decoder.decode(T.self, from: data)
    }
}

public struct AgentRoomClientIdentity: Equatable, Sendable {
    public var connectionID: String
    public var connectionLabel: String
    public var principalID: String
    public var principalLabel: String

    public init(
        connectionID: String,
        connectionLabel: String,
        principalID: String,
        principalLabel: String
    ) {
        self.connectionID = connectionID
        self.connectionLabel = connectionLabel
        self.principalID = principalID
        self.principalLabel = principalLabel
    }

    var queryItems: [URLQueryItem] {
        [
            URLQueryItem(name: "client_id", value: connectionID),
            URLQueryItem(name: "client_label", value: connectionLabel),
            URLQueryItem(name: "principal_id", value: principalID),
            URLQueryItem(name: "principal_label", value: principalLabel),
            URLQueryItem(name: "kind", value: "user")
        ]
    }
}
