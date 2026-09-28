// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract Vault {
    address public admin;
    address public emergencySigner;
    bool public paused;

    event EmergencyPaused(address indexed by);
    event Unpaused(address indexed by);

    modifier onlyEmergencySigner() {
        require(msg.sender == emergencySigner, "Caller is not emergency signer");
        _;
    }

    modifier onlyAdmin() {
        require(msg.sender == admin, "Caller is not admin");
        _;
    }

    modifier notPaused() {
        require(!paused, "Vault is paused");
        _;
    }

    constructor(address _admin, address _emergencySigner) {
        admin = _admin;
        emergencySigner = _emergencySigner;
        paused = false;
    }

    function emergencyPause() external onlyEmergencySigner {
        paused = true;
        emit EmergencyPaused(msg.sender);
    }

    function unpause() external onlyAdmin {
        paused = false;
        emit Unpaused(msg.sender);
    }

    function deposit() external payable notPaused {
        // deposit logic
    }

    function withdraw(uint256 amount) external notPaused {
        // withdrawal logic
    }
}
