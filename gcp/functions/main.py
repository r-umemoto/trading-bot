import os
import tweepy
import functions_framework
from google.events.cloud import firestore as firestoredata

# X (Twitter) APIの認証
def get_x_client():
    consumer_key = os.environ.get("X_CONSUMER_KEY")
    consumer_secret = os.environ.get("X_CONSUMER_SECRET")
    access_token = os.environ.get("X_ACCESS_TOKEN")
    access_token_secret = os.environ.get("X_ACCESS_TOKEN_SECRET")

    # Tweepy v2 Client (無料アカウントでの投稿にはv2が必須です)
    return tweepy.Client(
        consumer_key=consumer_key,
        consumer_secret=consumer_secret,
        access_token=access_token,
        access_token_secret=access_token_secret
    )

@functions_framework.cloud_event
def post_to_x(cloudevent):
    """GCP第2世代のEventarcトリガーで実行されるCloud Function
    """
    print(f"Event ID: {cloudevent['id']}")
    print(f"Event Type: {cloudevent['type']}")
    
    try:
        # google-events ライブラリを使って、APIアクセスなしで高速にバイナリ(Protobuf)を解読！
        print(f"CloudEvent data type: {type(cloudevent.data)}")
        event_data = firestoredata.DocumentEventData.deserialize(cloudevent.data)
        
        # to_dict でプレーン辞書化を試みる
        event_dict = None
        try:
            if hasattr(event_data, "to_dict"):
                event_dict = event_data.to_dict()
                print("Converted event_data to dict using instance.to_dict()")
            elif hasattr(firestoredata.DocumentEventData, "to_dict"):
                event_dict = firestoredata.DocumentEventData.to_dict(event_data)
                print("Converted event_data to dict using class.to_dict()")
        except Exception as e:
            print(f"Warning: Failed to convert event_data to dict: {e}")

        # 辞書化できた場合は辞書から抽出、できない場合は元のオブジェクトから抽出
        daily_report = None
        if event_dict:
            print(f"event_dict keys: {list(event_dict.keys())}")
            document = None
            if "value" in event_dict and event_dict["value"] and "fields" in event_dict["value"]:
                document = event_dict["value"]
                print("Found fields in event_dict['value']")
            elif "old_value" in event_dict and event_dict["old_value"] and "fields" in event_dict["old_value"]:
                document = event_dict["old_value"]
                print("Found fields in event_dict['old_value'] (document was deleted)")
            
            if document:
                daily_report = document["fields"]
            else:
                print("No fields found in event_dict.")
        
        if not daily_report:
            # 従来通りのオブジェクトによるアクセス
            document = None
            if hasattr(event_data, "value") and event_data.value and event_data.value.fields:
                document = event_data.value
                print("Found fields in event_data.value")
            elif hasattr(event_data, "old_value") and event_data.old_value and event_data.old_value.fields:
                document = event_data.old_value
                print("Found fields in event_data.old_value (document was deleted)")
            
            if not document or not document.fields:
                print("No fields found in the document value or old_value.")
                return "No fields", 400
                
            daily_report = document.fields
    except Exception as e:
        print(f"Failed to deserialize Firestore Eventarc data: {e}")
        return f"Deserialize Error: {e}", 400

    # _pb 属性があるかチェックして生の Protobuf オブジェクトを取得するヘルパー
    def get_pb(obj):
        if hasattr(obj, "_pb"):
            return obj._pb
        return obj

    # Protobufオブジェクトや辞書から値を取り出すための型解決ヘルパー
    def parse_value(value_obj):
        # 1. プレーンな辞書の場合（to_dict が成功した場合など）
        if isinstance(value_obj, dict):
            if 'string_value' in value_obj:
                return value_obj['string_value']
            if 'double_value' in value_obj:
                return value_obj['double_value']
            if 'integer_value' in value_obj:
                return value_obj['integer_value']
            if 'boolean_value' in value_obj:
                return value_obj['boolean_value']
            if 'array_value' in value_obj:
                arr = value_obj['array_value']
                if arr and 'values' in arr:
                    return [parse_value(v) for v in arr['values']]
            if 'map_value' in value_obj:
                m = value_obj['map_value']
                if m and 'fields' in m:
                    return parse_map(m['fields'])
            if 'null_value' in value_obj:
                return None
            if 'timestamp_value' in value_obj:
                return str(value_obj['timestamp_value'])
            return None

        # 2. 生の Protobuf オブジェクトを用いた ListFields() によるパース
        try:
            pb = get_pb(value_obj)
            fields = pb.ListFields()
            if fields:
                for descriptor, val in fields:
                    field_name = descriptor.name
                    if field_name in ('string_value', 'double_value', 'integer_value', 'boolean_value'):
                        return val
                    elif field_name == 'array_value':
                        if val and hasattr(val, 'values'):
                            return [parse_value(v) for v in val.values]
                    elif field_name == 'map_value':
                        if val and hasattr(val, 'fields'):
                            return parse_map(val.fields)
                        return {}
                    elif field_name == 'null_value':
                        return None
                    elif field_name == 'timestamp_value':
                        return str(val)
                    else:
                        return str(val)
        except Exception as e:
            print(f"Warning inside PB ListFields parsing: {e}")

        # 3. 生の Protobuf オブジェクトを用いた which_oneof() によるパース
        try:
            pb = get_pb(value_obj)
            if hasattr(pb, 'which_oneof'):
                val_type = pb.which_oneof('value_type')
                if val_type:
                    val = getattr(pb, val_type)
                    if val_type in ('string_value', 'double_value', 'integer_value', 'boolean_value'):
                        return val
                    elif val_type == 'array_value':
                        if val and hasattr(val, 'values'):
                            return [parse_value(v) for v in val.values]
                    elif val_type == 'map_value':
                        if val and hasattr(val, 'fields'):
                            return parse_map(val.fields)
                        return {}
                    elif val_type == 'null_value':
                        return None
                    elif val_type == 'timestamp_value':
                        return str(val)
                    else:
                        return str(val)
        except Exception as e:
            print(f"Warning inside PB which_oneof parsing: {e}")

        # 4. 万が一上記すべてで取得できない場合の最終フォールバック
        try:
            if hasattr(value_obj, 'string_value') and value_obj.string_value:
                return value_obj.string_value
            if hasattr(value_obj, 'double_value') and value_obj.double_value is not None:
                return value_obj.double_value
            if hasattr(value_obj, 'integer_value') and value_obj.integer_value is not None:
                return value_obj.integer_value
            if hasattr(value_obj, 'boolean_value') and value_obj.boolean_value is not None:
                return value_obj.boolean_value
            if hasattr(value_obj, 'array_value') and value_obj.array_value:
                return [parse_value(v) for v in value_obj.array_value.values]
            if hasattr(value_obj, 'map_value') and value_obj.map_value:
                return parse_map(value_obj.map_value.fields)
        except Exception as e:
            print(f"Warning inside final fallback: {e}")
        return None

    def parse_map(fields_obj):
        if isinstance(fields_obj, dict):
            return {k: parse_value(v) for k, v in fields_obj.items()}
        try:
            return {k: parse_value(v) for k, v in fields_obj.items()}
        except Exception:
            return {}

    try:
        # データを解析
        report_data = parse_map(daily_report)
        print(f"Successfully deserialized report data: {report_data}")
    except Exception as e:
        print(f"Failed to parse fields: {e}")
        return f"Parse Error: {e}", 500

    # Xへの投稿テキストの生成
    try:
        tweet_text = generate_tweet(report_data)
        # Cloud Loggingで改行ごとに分割されないよう、改行をエスケープして1行で出力します
        # (Python 3.11以下のf-string制約を避けるため、外側で置換を行ってから出力します)
        compact_tweet = tweet_text.replace(chr(10), '\\n')
        print(f"Generated tweet (compact): {compact_tweet}")

        # Xに投稿
        client = get_x_client()
        response = client.create_tweet(text=tweet_text)
        print(f"Successfully tweeted! Tweet ID: {response.data['id']}")
    except Exception as e:
        print(f"Error during tweeting: {e}")
        return f"Error: {e}", 500

    return "Successfully tweeted", 200

def generate_tweet(daily_report):
    total = daily_report.get("total", {})
    total_pnl = total.get("total_pnl", 0.0) if isinstance(total, dict) else 0.0
    
    sign = "+" if total_pnl >= 0 else ""
    
    # 140文字制限（全角）を確実にクリアするための超コンパクト＆リッチな設計
    tweet = f"💰本日の合計損益: {sign}{total_pnl:,.0f}円\n\n"
    
    # 有効な戦略データを抽出
    valid_strats = []
    strats = daily_report.get("strats", [])
    if isinstance(strats, list):
        for strat in strats:
            if not isinstance(strat, dict):
                continue
            valid_strats.append({
                "name": strat.get("name", "Unknown"),
                "total_pnl": float(strat.get("total_pnl", 0.0))
            })
            
    strats_parts = []
    if valid_strats:
        # 損益の低い順（昇順）にソート
        sorted_strats = sorted(valid_strats, key=lambda x: x["total_pnl"])
        
        # 1つの戦略しかない場合はそのままだす
        if len(sorted_strats) == 1:
            strat = sorted_strats[0]
            pnl_sign = "+" if strat["total_pnl"] >= 0 else ""
            strats_parts.append(f"・{strat['name']}: {pnl_sign}{strat['total_pnl']:,.0f}円")
        else:
            worst = sorted_strats[0]
            best = sorted_strats[-1]
            
            pnl_sign_best = "+" if best["total_pnl"] >= 0 else ""
            pnl_sign_worst = "+" if worst["total_pnl"] >= 0 else ""
            
            strats_parts.append(f"🏆ベスト: {best['name']} ({pnl_sign_best}{best['total_pnl']:,.0f}円)")
            strats_parts.append(f"📉ワースト: {worst['name']} ({pnl_sign_worst}{worst['total_pnl']:,.0f}円)")
            
    if strats_parts:
        tweet += "📊ハイライト:\n" + "\n".join(strats_parts) + "\n\n"
        
    tweet += "#シストレ #自動売買"
    
    # 万が一135文字を超えていた場合のセーフティ
    if len(tweet) > 135:
        tweet_no_hash = tweet.split("#")[0].strip()
        if len(tweet_no_hash) <= 135:
            tweet = tweet_no_hash
        else:
            tweet = tweet_no_hash[:130] + "..."
            
    return tweet


