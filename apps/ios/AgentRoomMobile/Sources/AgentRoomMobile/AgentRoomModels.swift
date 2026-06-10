import Foundation

public enum AgentRoomMessageType: String, Codable, Sendable {
    case chat
    case command
    case commandResult = "command_result"
    case presence
    case system
    case trace
    case control
}

public enum AgentRoomSenderKind: String, Codable, Sendable {
    case user
    case agent
    case system
}

public struct AgentRoom: Codable, Identifiable, Equatable, Sendable {
    public let roomID: String
    public let owner: String?
    public let title: String?
    public let gated: Bool
    public let ended: Bool
    public let createdAt: Date

    public var id: String { roomID }

    enum CodingKeys: String, CodingKey {
        case roomID = "room_id"
        case owner
        case title
        case gated
        case ended
        case createdAt = "created_at"
    }
}

public struct AgentRoomMessage: Codable, Identifiable, Equatable, Sendable {
    public let id: String
    public let roomID: String
    public let type: AgentRoomMessageType
    public let senderID: String
    public let senderKind: AgentRoomSenderKind
    public let targetID: String?
    public let content: String
    public let replyRequested: Bool
    public let turnBudget: Int
    public let createdAt: Date
    public let metadata: [String: String]?

    enum CodingKeys: String, CodingKey {
        case id
        case roomID = "room_id"
        case type
        case senderID = "sender_id"
        case senderKind = "sender_kind"
        case targetID = "target_id"
        case content
        case replyRequested = "reply_requested"
        case turnBudget = "turn_budget"
        case createdAt = "created_at"
        case metadata
    }
}

public struct AgentRoomParticipant: Codable, Identifiable, Equatable, Sendable {
    public let id: String
    public let roomID: String
    public let kind: AgentRoomSenderKind
    public let label: String
    public let connectionID: String?
    public let connectionCount: Int?
    public let connections: [AgentRoomParticipantConnection]?
    public let connectedAt: Date
    public let lastSeenAt: Date
    public let metadata: [String: String]?

    enum CodingKeys: String, CodingKey {
        case id
        case roomID = "room_id"
        case kind
        case label
        case connectionID = "connection_id"
        case connectionCount = "connection_count"
        case connections
        case connectedAt = "connected_at"
        case lastSeenAt = "last_seen_at"
        case metadata
    }
}

public struct AgentRoomParticipantConnection: Codable, Equatable, Sendable {
    public let id: String
    public let label: String?
    public let connectedAt: Date
    public let lastSeenAt: Date
    public let metadata: [String: String]?

    enum CodingKeys: String, CodingKey {
        case id
        case label
        case connectedAt = "connected_at"
        case lastSeenAt = "last_seen_at"
        case metadata
    }
}

public struct AgentRoomUser: Codable, Equatable, Sendable {
    public let login: String
    public let name: String
    public let email: String
    public let avatarURL: String

    enum CodingKeys: String, CodingKey {
        case login
        case name
        case email
        case avatarURL = "avatar_url"
    }
}

public struct AgentRoomMe: Codable, Equatable, Sendable {
    public let authenticated: Bool
    public let authEnabled: Bool
    public let loginURL: String?
    public let authProvider: String?
    public let user: AgentRoomUser?
    public let isAdmin: Bool?

    enum CodingKeys: String, CodingKey {
        case authenticated
        case authEnabled = "auth_enabled"
        case loginURL = "login_url"
        case authProvider = "auth_provider"
        case user
        case isAdmin = "is_admin"
    }
}

public struct AgentRoomOutgoingMessage: Codable, Equatable, Sendable {
    public var roomID: String
    public var type: AgentRoomMessageType
    public var senderID: String
    public var senderKind: AgentRoomSenderKind
    public var targetID: String?
    public var content: String
    public var replyRequested: Bool
    public var turnBudget: Int
    public var metadata: [String: String]?

    public init(
        roomID: String,
        type: AgentRoomMessageType = .chat,
        senderID: String,
        senderKind: AgentRoomSenderKind = .user,
        targetID: String? = nil,
        content: String,
        replyRequested: Bool = false,
        turnBudget: Int = 0,
        metadata: [String: String]? = nil
    ) {
        self.roomID = roomID
        self.type = type
        self.senderID = senderID
        self.senderKind = senderKind
        self.targetID = targetID
        self.content = content
        self.replyRequested = replyRequested
        self.turnBudget = turnBudget
        self.metadata = metadata
    }

    enum CodingKeys: String, CodingKey {
        case roomID = "room_id"
        case type
        case senderID = "sender_id"
        case senderKind = "sender_kind"
        case targetID = "target_id"
        case content
        case replyRequested = "reply_requested"
        case turnBudget = "turn_budget"
        case metadata
    }
}
