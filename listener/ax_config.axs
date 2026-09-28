/// BeaconServerless

function ListenerUI(mode_create)
{
    let labelRegion = form.create_label("AWS Region:");
    let editRegion = form.create_textline();
    editRegion.setPlaceholder("us-east-1");
    editRegion.setText("us-east-1");

    let labelLambdaURL = form.create_label("Lambda URL:");
    let editLambdaURL = form.create_textline();
    editLambdaURL.setPlaceholder("https://xxx.lambda-url.us-east-1.on.aws/relay");
    editLambdaURL.setEnabled(mode_create);

    let labelRelayKey = form.create_label("Relay API Key:");
    let editRelayKey = form.create_textline();
    editRelayKey.setPlaceholder("API key for Lambda relay authentication");
    editRelayKey.setEnabled(mode_create);

    let labelProfile = form.create_label("Malleable Profile:");
    let editProfile = form.create_selector_file();
    editProfile.setPlaceholder("Select a profile file");
    editProfile.setEnabled(mode_create);

    let labelInbound = form.create_label("Inbound Table:");
    let editInbound = form.create_textline();
    editInbound.setPlaceholder("adaptix-inbound");
    editInbound.setText("adaptix-inbound");
    editInbound.setEnabled(mode_create);

    let labelOutbound = form.create_label("Outbound Table:");
    let editOutbound = form.create_textline();
    editOutbound.setPlaceholder("adaptix-outbound");
    editOutbound.setText("adaptix-outbound");
    editOutbound.setEnabled(mode_create);

    let labelAgents = form.create_label("Agents Table:");
    let editAgents = form.create_textline();
    editAgents.setPlaceholder("adaptix-agents");
    editAgents.setText("adaptix-agents");
    editAgents.setEnabled(mode_create);

    let labelPoll = form.create_label("Poll Interval (s):");
    let spinPoll = form.create_spin();
    spinPoll.setRange(1, 3600);
    spinPoll.setValue(5);

    let labelEncKey = form.create_label("Encrypt Key:");
    let editEncKey = form.create_textline();
    editEncKey.setPlaceholder("32-char hex, auto-generated if empty");

    let labelTTL = form.create_label("TTL Hours:");
    let spinTTL = form.create_spin();
    spinTTL.setRange(1, 720);
    spinTTL.setValue(24);

    let layout = form.create_gridlayout();
    layout.addWidget(labelRegion,    0, 0, 1, 1);
    layout.addWidget(editRegion,     0, 1, 1, 1);
    layout.addWidget(labelLambdaURL, 1, 0, 1, 1);
    layout.addWidget(editLambdaURL,  1, 1, 1, 1);
    layout.addWidget(labelRelayKey,  2, 0, 1, 1);
    layout.addWidget(editRelayKey,   2, 1, 1, 1);
    layout.addWidget(labelProfile,   3, 0, 1, 1);
    layout.addWidget(editProfile,    3, 1, 1, 1);
    layout.addWidget(labelInbound,   4, 0, 1, 1);
    layout.addWidget(editInbound,    4, 1, 1, 1);
    layout.addWidget(labelOutbound,  5, 0, 1, 1);
    layout.addWidget(editOutbound,   5, 1, 1, 1);
    layout.addWidget(labelAgents,    6, 0, 1, 1);
    layout.addWidget(editAgents,     6, 1, 1, 1);
    layout.addWidget(labelPoll,      7, 0, 1, 1);
    layout.addWidget(spinPoll,       7, 1, 1, 1);
    layout.addWidget(labelEncKey,    8, 0, 1, 1);
    layout.addWidget(editEncKey,     8, 1, 1, 1);
    layout.addWidget(labelTTL,       9, 0, 1, 1);
    layout.addWidget(spinTTL,        9, 1, 1, 1);

    let container = form.create_container();
    container.put("region",         editRegion);
    container.put("lambda_url",     editLambdaURL);
    container.put("relay_api_key",  editRelayKey);
    container.put("uploaded_file",  editProfile);
    container.put("inbound_table",  editInbound);
    container.put("outbound_table", editOutbound);
    container.put("agents_table",   editAgents);
    container.put("poll_interval",  spinPoll);
    container.put("encrypt_key",    editEncKey);
    container.put("ttl_hours",      spinTTL);

    let panel = form.create_panel();
    panel.setLayout(layout);

    return {
        ui_panel: panel,
        ui_container: container,
        ui_height: 620,
        ui_width: 560
    }
}
