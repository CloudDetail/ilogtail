// Copyright 2023 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#include <cstdlib>
#include "unittest/Unittest.h"
#include "common/JsonUtil.h"
#include "models/LogEvent.h"

namespace logtail {

class LogEventUnittest : public ::testing::Test {
public:
    void SetUp() override {
        mSourceBuffer.reset(new SourceBuffer);
        mLogEvent = LogEvent::CreateEvent(mSourceBuffer);
    }

    void TestSetTimestamp();
    void TestSetContent();
    void TestDelContent();
    void TestReadOp();
    void TestIterate();

    void TestFromJsonToJson();

protected:
    std::shared_ptr<SourceBuffer> mSourceBuffer;
    std::unique_ptr<LogEvent> mLogEvent;
};

APSARA_UNIT_TEST_CASE(LogEventUnittest, TestSetTimestamp, 0);
APSARA_UNIT_TEST_CASE(LogEventUnittest, TestSetContent, 0);
APSARA_UNIT_TEST_CASE(LogEventUnittest, TestDelContent, 0);
APSARA_UNIT_TEST_CASE(LogEventUnittest, TestReadOp, 0);
APSARA_UNIT_TEST_CASE(LogEventUnittest, TestIterate, 0);
APSARA_UNIT_TEST_CASE(LogEventUnittest, TestFromJsonToJson, 0);

void LogEventUnittest::TestSetTimestamp() {
    mLogEvent->SetTimestamp(13800000000);
    APSARA_TEST_EQUAL_FATAL(13800000000, mLogEvent->GetTimestamp());
}

void LogEventUnittest::TestSetContent() {
    { // string copy, let kv out of scope
        mLogEvent->SetContent(std::string("key1"), std::string("value1"));
    }
    { // stringview copy, let kv out of scope
        std::string key("key2");
        std::string value("value2");
        mLogEvent->SetContent(StringView(key), StringView(value));
    }
    size_t beforeAlloc;
    { // StringBuffer nocopy
        StringBuffer key = mLogEvent->GetSourceBuffer()->CopyString(std::string("key3"));
        StringBuffer value = mLogEvent->GetSourceBuffer()->CopyString(std::string("value3"));
        beforeAlloc = mSourceBuffer->mAllocator.TotalAllocated();
        mLogEvent->SetContentNoCopy(key, value);
    }
    std::string key("key4");
    std::string value("value4");
    { // StringView nocopy
        mLogEvent->SetContentNoCopy(StringView(key), StringView(value));
    }
    size_t afterAlloc = mSourceBuffer->mAllocator.TotalAllocated();
    APSARA_TEST_EQUAL_FATAL(beforeAlloc, afterAlloc);
    std::vector<std::pair<std::string, std::string>> answers = {
        {"key1", "value1"},
        {"key2", "value2"},
        {"key3", "value3"},
        {"key4", "value4"},
    };
    for (const auto kv : answers) {
        APSARA_TEST_TRUE_FATAL(mLogEvent->HasContent(kv.first));
        APSARA_TEST_STREQ_FATAL(kv.second.c_str(), mLogEvent->GetContent(kv.first).data());
    }
}

void LogEventUnittest::TestDelContent() {
    mLogEvent->SetContent(std::string("key1"), std::string("value1"));
    {
        // key not exists
        mLogEvent->DelContent(std::string("key2"));
        APSARA_TEST_EQUAL(1, mLogEvent->Size());
    }
    {
        // key exists
        mLogEvent->DelContent(std::string("key1"));
        APSARA_TEST_FALSE(mLogEvent->HasContent("key1"));
        APSARA_TEST_TRUE(mLogEvent->Empty());
    }
}

void LogEventUnittest::TestReadOp() {
    mLogEvent->SetContent(std::string("key1"), std::string("value1"));
    {
        // key not exists
        auto it = mLogEvent->FindContent("key2");
        APSARA_TEST_TRUE(it == mLogEvent->end());
        APSARA_TEST_FALSE(mLogEvent->HasContent("key2"));
        APSARA_TEST_EQUAL(nullptr, mLogEvent->GetContent("key2").data());
    }
    {
        // key exists
        auto it = mLogEvent->FindContent("key1");
        APSARA_TEST_TRUE(it != mLogEvent->end());
        APSARA_TEST_STREQ("key1", it->first.data());
        APSARA_TEST_STREQ("value1", it->second.data());
        APSARA_TEST_TRUE(mLogEvent->HasContent("key1"));
        APSARA_TEST_STREQ("value1", mLogEvent->GetContent("key1").data());
    }
}

void LogEventUnittest::TestIterate() {
    {
        // first element is valid
        mLogEvent->SetContent(std::string("key1"), std::string("value1"));
        APSARA_TEST_STREQ("key1", mLogEvent->begin()->first.data());
        APSARA_TEST_STREQ("value1", mLogEvent->begin()->second.data());
    }
    {
        // first element is invalid, and no element is valid
        mLogEvent->DelContent("key1");
        APSARA_TEST_TRUE(mLogEvent->end() == mLogEvent->begin());
    }
    {
        // first element is invalid, and at least one element is valid
        mLogEvent->SetContent(std::string("key1"), std::string("value1"));
        APSARA_TEST_STREQ("key1", mLogEvent->begin()->first.data());
        APSARA_TEST_STREQ("value1", mLogEvent->begin()->second.data());
    }

    // 2 valid elements and 1 invalid element
    mLogEvent->SetContent(std::string("key2"), std::string("value2"));
    {
        // suffix iteration
        auto it = mLogEvent->begin()++;
        APSARA_TEST_STREQ("key1", it->first.data());
        APSARA_TEST_STREQ("value1", it->second.data());
    }
    {
        // prefix iteration
        auto it = ++mLogEvent->begin();
        APSARA_TEST_STREQ("key2", it->first.data());
        APSARA_TEST_STREQ("value2", it->second.data());
        APSARA_TEST_TRUE(mLogEvent->end() == ++it);
    }
}

void LogEventUnittest::TestFromJsonToJson() {
    std::string inJson = R"({
        "contents" :
        {
            "key1" : "value1",
            "key2" : "value2"
        },
        "timestamp" : 12345678901,
        "timestampNanosecond" : 0,
        "type" : 1
    })";
    APSARA_TEST_TRUE_FATAL(mLogEvent->FromJsonString(inJson));
    APSARA_TEST_EQUAL_FATAL(12345678901L, mLogEvent->GetTimestamp());
    std::vector<std::pair<std::string, std::string>> answers = {{"key1", "value1"}, {"key2", "value2"}};
    for (const auto kv : answers) {
        APSARA_TEST_TRUE_FATAL(mLogEvent->HasContent(kv.first));
        APSARA_TEST_STREQ_FATAL(kv.second.c_str(), mLogEvent->GetContent(kv.first).data());
    }
    std::string outJson = mLogEvent->ToJsonString();
    APSARA_TEST_STREQ_FATAL(CompactJson(inJson).c_str(), CompactJson(outJson).c_str());
}

} // namespace logtail

UNIT_TEST_MAIN