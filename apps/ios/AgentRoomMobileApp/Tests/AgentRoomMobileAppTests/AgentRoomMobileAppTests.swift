@testable import AgentRoomMobileApp
import XCTest

final class AgentRoomMobileAppTests: XCTestCase {
    func testDefaultIdentityUsesMobilePrefix() {
        let identity = AgentRoomClientIdentity(
            connectionID: "ios-test",
            connectionLabel: "iPhone",
            principalID: "ios-test",
            principalLabel: "iPhone"
        )

        XCTAssertEqual(identity.connectionID, "ios-test")
        XCTAssertEqual(identity.principalLabel, "iPhone")
    }
}
